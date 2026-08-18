package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// Compile-time assertions.
var (
	_ router.Consumer     = (*VectorSinkConsumer)(nil)
	_ router.BatchFlusher = (*VectorSinkConsumer)(nil)
)

// textHashCache is the VEC-01 hash-cache surface used by the consumer.
// *HashCache implements it; tests may inject spies.
type textHashCache interface {
	Unchanged(id string, hash []byte) bool
	Put(id string, hash []byte) error
	PutBatch(ids []string, hashes [][]byte) error
	Del(id string) error
	DelBatch(ids []string) error
	Close() error
}

// VectorSinkConsumer is a router.Consumer that extracts text, embeds it, and
// upserts/deletes vectors. Deliver only buffers; FlushBatch performs I/O
// (CHK-01 provisional cursor). Hash-cache writes happen only after store ack
// (VEC-01/VEC-03). Consecutive upsert/delete runs preserve per-key order (VEC-02).
type VectorSinkConsumer struct {
	id           string
	extractor    *Extractor
	embedder     Embedder
	store        VectorStore
	cache        textHashCache
	metadataCols []string
	batchMax     int

	mu         sync.Mutex
	pending    map[uint32][]pendingVec
	pendingIDs map[uint32]map[string]struct{}
	m          *observability.KaptantoMetrics
}

// pendingVec is one buffered event ready for FlushBatch.
type pendingVec struct {
	seq      uint64
	id       string
	text     string
	hash     []byte
	metadata map[string]any
	delete   bool
}

// NewVectorSinkConsumer constructs a VectorSinkConsumer from cfg.
// Secrets (${VAR}) are expanded; dataDir holds the VEC-01 hash-cache SQLite file.
func NewVectorSinkConsumer(id string, cfg config.VectorSinkConfig, dataDir string) (*VectorSinkConsumer, error) {
	if err := Validate(&cfg); err != nil {
		return nil, err
	}
	expanded, err := expandVectorConfig(cfg)
	if err != nil {
		return nil, err
	}

	extractor, err := NewExtractor(expanded.Source)
	if err != nil {
		return nil, err
	}
	embedder, err := NewEmbedder(expanded.Embedder, nil)
	if err != nil {
		return nil, err
	}

	dims := expanded.Embedder.Dimensions
	switch expanded.Store.Provider {
	case "pgvector", "qdrant":
		if dims <= 0 {
			return nil, fmt.Errorf("vector: embedder.dimensions is required for store provider %q", expanded.Store.Provider)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := OpenStore(ctx, expanded.Store, dims)
	if err != nil {
		return nil, fmt.Errorf("vector sink: open store: %w", err)
	}

	cache := OpenHashCache(dataDir)
	return NewVectorSinkConsumerWithDeps(id, extractor, embedder, store, cache, expanded.Metadata, expanded.Batch.MaxEvents), nil
}

// NewVectorSinkConsumerWithDeps constructs a consumer with already-built deps.
// Used by tests and by NewVectorSinkConsumer after construction.
func NewVectorSinkConsumerWithDeps(
	id string,
	extractor *Extractor,
	embedder Embedder,
	store VectorStore,
	cache textHashCache,
	metadata []string,
	batchMax int,
) *VectorSinkConsumer {
	if batchMax <= 0 {
		batchMax = DefaultBatchMaxEvents
	}
	meta := make([]string, len(metadata))
	copy(meta, metadata)
	return &VectorSinkConsumer{
		id:           id,
		extractor:    extractor,
		embedder:     embedder,
		store:        store,
		cache:        cache,
		metadataCols: meta,
		batchMax:     batchMax,
		pending:      make(map[uint32][]pendingVec),
		pendingIDs:   make(map[uint32]map[string]struct{}),
	}
}

func expandVectorConfig(cfg config.VectorSinkConfig) (config.VectorSinkConfig, error) {
	expandSecret := func(s string) (string, error) {
		if strings.TrimSpace(s) == "" {
			return "", nil
		}
		var missing []string
		out := os.Expand(s, func(name string) string {
			v, ok := os.LookupEnv(name)
			if !ok {
				missing = append(missing, name)
				return ""
			}
			return v
		})
		if len(missing) > 0 {
			return "", fmt.Errorf("vector sink: missing environment variable(s): %s", strings.Join(missing, ", "))
		}
		return out, nil
	}
	expand := func(s string) string { return os.Expand(s, os.Getenv) }

	var err error
	if cfg.Embedder.APIKey, err = expandSecret(cfg.Embedder.APIKey); err != nil {
		return cfg, err
	}
	cfg.Embedder.BaseURL = expand(cfg.Embedder.BaseURL)
	if cfg.Store.DSN, err = expandSecret(cfg.Store.DSN); err != nil {
		return cfg, err
	}
	if cfg.Store.APIKey, err = expandSecret(cfg.Store.APIKey); err != nil {
		return cfg, err
	}
	cfg.Store.IndexHost = expand(cfg.Store.IndexHost)
	cfg.Store.URL = expand(cfg.Store.URL)
	cfg.Store.Namespace = expand(cfg.Store.Namespace)
	cfg.Store.Table = expand(cfg.Store.Table)
	cfg.Store.Collection = expand(cfg.Store.Collection)
	return cfg, nil
}

// ID returns the stable consumer identifier.
func (c *VectorSinkConsumer) ID() string { return c.id }

// SetMetrics injects metrics. Call after construction, before Deliver.
func (c *VectorSinkConsumer) SetMetrics(m *observability.KaptantoMetrics) { c.m = m }

// Deliver buffers entry for FlushBatch. No embedder/store I/O happens here.
func (c *VectorSinkConsumer) Deliver(ctx context.Context, entry eventlog.LogEntry) error {
	if err := entry.MaterializeEvent(); err != nil {
		return fmt.Errorf("vector sink: materialize event: %w", err)
	}
	ev := entry.Event

	if ev.Operation == event.OpControl {
		return nil
	}

	id, err := CanonicalIDFromRaw(ev.Schema, ev.Table, ev.Key)
	if err != nil {
		return fmt.Errorf("vector sink: canonical id: %w", err)
	}

	if ev.Operation == event.OpDelete {
		c.mu.Lock()
		if c.pendingIDs[entry.PartitionID] == nil {
			c.pendingIDs[entry.PartitionID] = make(map[string]struct{})
		}
		c.pendingIDs[entry.PartitionID][id] = struct{}{}
		c.pending[entry.PartitionID] = append(c.pending[entry.PartitionID], pendingVec{
			seq:    entry.Seq,
			id:     id,
			delete: true,
		})
		c.mu.Unlock()
		return nil
	}

	text, err := c.extractor.Extract(ev)
	if err != nil {
		return err // ExtractError → PermanentDelivery → immediate DLQ
	}
	if text == "" {
		c.incSkipped(SkipReasonEmpty)
		return nil
	}

	hash := HashText(text)
	c.mu.Lock()
	hasPending := false
	if ids := c.pendingIDs[entry.PartitionID]; ids != nil {
		_, hasPending = ids[id]
	}
	if c.cache != nil && c.cache.Unchanged(id, hash) && !hasPending {
		c.mu.Unlock()
		c.incSkipped(SkipReasonUnchanged)
		return nil
	}
	if c.pendingIDs[entry.PartitionID] == nil {
		c.pendingIDs[entry.PartitionID] = make(map[string]struct{})
	}
	c.pendingIDs[entry.PartitionID][id] = struct{}{}
	c.pending[entry.PartitionID] = append(c.pending[entry.PartitionID], pendingVec{
		seq:      entry.Seq,
		id:       id,
		text:     text,
		hash:     hash,
		metadata: buildMetadata(ev, c.metadataCols),
		delete:   false,
	})
	c.mu.Unlock()
	return nil
}

func (c *VectorSinkConsumer) incSkipped(reason string) {
	if c.m != nil {
		c.m.VectorSkippedTotal.WithLabelValues(reason).Inc()
	}
}

func buildMetadata(ev *event.ChangeEvent, cols []string) map[string]any {
	meta := map[string]any{
		"table":     ev.Table,
		"operation": string(ev.Operation),
		"timestamp": ev.Timestamp.UTC().Format(time.RFC3339Nano),
	}
	if ev.Schema != "" {
		meta["schema"] = ev.Schema
	}
	if len(cols) == 0 || len(ev.After) == 0 {
		return meta
	}
	var after map[string]any
	if err := json.Unmarshal(ev.After, &after); err != nil {
		return meta
	}
	for _, col := range cols {
		if v, ok := after[col]; ok {
			meta[col] = v
		}
	}
	return meta
}

// FlushBatch pops the pending buffer for partitionID and executes consecutive
// upsert/delete runs in order (VEC-02).
func (c *VectorSinkConsumer) FlushBatch(ctx context.Context, partitionID uint32) error {
	c.mu.Lock()
	if len(c.pending[partitionID]) == 0 {
		c.mu.Unlock()
		return nil
	}
	batch := c.pending[partitionID]
	delete(c.pending, partitionID)
	delete(c.pendingIDs, partitionID)
	c.mu.Unlock()

	start := time.Now()
	var successCount, errorCount int
	var firstErr error

	state := &flushEmbedCache{vectors: make(map[string][]float32)}
	for _, r := range splitRuns(batch) {
		var err error
		if r.delete {
			err = c.flushDeleteRun(ctx, r.items)
		} else {
			err = c.flushUpsertRun(ctx, r.items, state)
		}
		if err != nil {
			errorCount++
			firstErr = err
			break
		}
		successCount++
	}

	if c.m != nil {
		c.m.QueuePublishLatency.WithLabelValues("vector").Observe(time.Since(start).Seconds())
		if successCount > 0 {
			c.m.QueuePublishTotal.WithLabelValues("vector").Add(float64(successCount))
		}
		if errorCount > 0 {
			c.m.QueuePublishErrors.WithLabelValues("vector").Add(float64(errorCount))
		}
	}
	return firstErr
}

type vecRun struct {
	delete bool
	items  []pendingVec
}

// splitRuns groups consecutive upserts and deletes (VEC-02 order boundary).
func splitRuns(batch []pendingVec) []vecRun {
	var runs []vecRun
	for _, item := range batch {
		if len(runs) == 0 || runs[len(runs)-1].delete != item.delete {
			runs = append(runs, vecRun{delete: item.delete, items: []pendingVec{item}})
			continue
		}
		runs[len(runs)-1].items = append(runs[len(runs)-1].items, item)
	}
	return runs
}

// flushEmbedCache holds embeddings produced during a single FlushBatch attempt
// so poison isolation does not re-embed already-embedded vectors. Keys combine
// id and text hash so consecutive same-id upserts with different text each embed.
type flushEmbedCache struct {
	vectors map[string][]float32
}

func embedCacheKey(it pendingVec) string {
	return it.id + "\x00" + string(it.hash)
}

func (c *VectorSinkConsumer) flushDeleteRun(ctx context.Context, items []pendingVec) error {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.id
	}
	if err := c.store.Delete(ctx, ids); err != nil {
		if IsPoison(err) && len(items) > 1 {
			return c.isolateDelete(ctx, items)
		}
		return c.classifyStoreErr(err, items)
	}
	c.ackDeletes(items)
	return nil
}

func (c *VectorSinkConsumer) isolateDelete(ctx context.Context, items []pendingVec) error {
	for _, it := range items {
		if err := c.store.Delete(ctx, []string{it.id}); err != nil {
			if IsPoison(err) {
				return &router.PermanentFlushError{Seq: it.seq, Cause: err}
			}
			return err // transient — abort isolation
		}
		c.ackDeletes([]pendingVec{it})
	}
	return nil
}

func (c *VectorSinkConsumer) ackDeletes(items []pendingVec) {
	if c.cache != nil && len(items) > 0 {
		ids := make([]string, len(items))
		for i, it := range items {
			ids[i] = it.id
		}
		if err := c.cache.DelBatch(ids); err != nil {
			slog.Warn("vector: hash cache del batch failed after store ack", "count", len(ids), "err", err)
		}
	}
	if c.m != nil {
		c.m.VectorDeletesTotal.Add(float64(len(items)))
	}
}

func (c *VectorSinkConsumer) flushUpsertRun(ctx context.Context, items []pendingVec, state *flushEmbedCache) error {
	chunkSize := c.batchMax
	if cap := c.embedder.Cap(); cap > 0 && cap < chunkSize {
		chunkSize = cap
	}
	if chunkSize < 1 {
		chunkSize = 1
	}
	for i := 0; i < len(items); i += chunkSize {
		end := i + chunkSize
		if end > len(items) {
			end = len(items)
		}
		if err := c.flushUpsertChunk(ctx, items[i:end], state); err != nil {
			return err
		}
	}
	return nil
}

func (c *VectorSinkConsumer) flushUpsertChunk(ctx context.Context, items []pendingVec, state *flushEmbedCache) error {
	if err := c.ensureEmbedded(ctx, items, state); err != nil {
		if IsPoison(err) && len(items) > 1 {
			return c.isolateUpsert(ctx, items, state)
		}
		return c.wrapEmbedErr(err, items)
	}
	recs := make([]Record, len(items))
	for i, it := range items {
		recs[i] = Record{
			ID:       it.id,
			Vector:   state.vectors[embedCacheKey(it)],
			Metadata: it.metadata,
			Text:     it.text,
		}
	}
	if err := c.store.Upsert(ctx, recs); err != nil {
		if IsPoison(err) && len(items) > 1 {
			return c.isolateUpsert(ctx, items, state)
		}
		return c.classifyStoreErr(err, items)
	}
	c.ackUpserts(items)
	return nil
}

func (c *VectorSinkConsumer) ensureEmbedded(ctx context.Context, items []pendingVec, state *flushEmbedCache) error {
	var need []pendingVec
	var texts []string
	for _, it := range items {
		if _, ok := state.vectors[embedCacheKey(it)]; ok {
			continue
		}
		need = append(need, it)
		texts = append(texts, it.text)
	}
	if len(texts) == 0 {
		return nil
	}
	start := time.Now()
	vecs, err := c.embedder.Embed(ctx, texts)
	if c.m != nil {
		c.m.VectorEmbedLatency.Observe(time.Since(start).Seconds())
	}
	if err != nil {
		return err
	}
	if len(vecs) != len(texts) {
		return fmt.Errorf("vector: embedder returned %d vectors for %d texts (VEC-02)", len(vecs), len(texts))
	}
	for i, it := range need {
		state.vectors[embedCacheKey(it)] = vecs[i]
	}
	if c.m != nil {
		c.m.VectorEmbeddedTotal.Add(float64(len(texts)))
	}
	return nil
}

// isolateUpsert re-sends one event at a time (isolation, not retry). Embeddings
// already in state are reused so successes are not re-embedded.
func (c *VectorSinkConsumer) isolateUpsert(ctx context.Context, items []pendingVec, state *flushEmbedCache) error {
	for _, it := range items {
		if err := c.ensureEmbedded(ctx, []pendingVec{it}, state); err != nil {
			if IsPoison(err) {
				return &router.PermanentFlushError{Seq: it.seq, Cause: err}
			}
			return err // transient — abort isolation
		}
		rec := Record{
			ID:       it.id,
			Vector:   state.vectors[embedCacheKey(it)],
			Metadata: it.metadata,
			Text:     it.text,
		}
		if err := c.store.Upsert(ctx, []Record{rec}); err != nil {
			if IsPoison(err) {
				return &router.PermanentFlushError{Seq: it.seq, Cause: err}
			}
			return err // transient — abort isolation
		}
		c.ackUpserts([]pendingVec{it})
	}
	return nil
}

func (c *VectorSinkConsumer) ackUpserts(items []pendingVec) {
	if c.cache != nil && len(items) > 0 {
		ids := make([]string, 0, len(items))
		hashes := make([][]byte, 0, len(items))
		for _, it := range items {
			if len(it.hash) == 0 {
				continue
			}
			ids = append(ids, it.id)
			hashes = append(hashes, it.hash)
		}
		if len(ids) > 0 {
			if err := c.cache.PutBatch(ids, hashes); err != nil {
				slog.Warn("vector: hash cache put batch failed after store ack", "count", len(ids), "err", err)
			}
		}
	}
	if c.m != nil {
		c.m.VectorUpsertsTotal.Add(float64(len(items)))
	}
}

func (c *VectorSinkConsumer) wrapEmbedErr(err error, items []pendingVec) error {
	if IsPoison(err) && len(items) == 1 {
		return &router.PermanentFlushError{Seq: items[0].seq, Cause: err}
	}
	return err
}

func (c *VectorSinkConsumer) classifyStoreErr(err error, items []pendingVec) error {
	if IsPoison(err) && len(items) >= 1 {
		return &router.PermanentFlushError{Seq: items[0].seq, Cause: err}
	}
	return err
}

// Ping probes the vector store (embedder is a no-op).
func (c *VectorSinkConsumer) Ping() error {
	if c.store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.store.Ping(ctx)
}

// Close releases the store and hash cache. Safe to call multiple times.
func (c *VectorSinkConsumer) Close() {
	if c.store != nil {
		if err := c.store.Close(); err != nil {
			slog.Warn("vector: store close", "err", err)
		}
		c.store = nil
	}
	if c.cache != nil {
		if err := c.cache.Close(); err != nil {
			slog.Warn("vector: hash cache close", "err", err)
		}
		c.cache = nil
	}
}
