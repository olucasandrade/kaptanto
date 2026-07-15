# @kaptanto/events

TypeScript types for the [Kaptanto](https://github.com/olucasandrade/kaptanto) ChangeEvent wire format.

## Install

```bash
npm install @kaptanto/events
```

## Usage

```typescript
import { type ChangeEvent, isChangeEvent, isInsert } from "@kaptanto/events";

const raw: unknown = JSON.parse(line);

if (isChangeEvent(raw)) {
  console.log(raw.table, raw.operation);

  if (isInsert(raw)) {
    console.log("new row:", raw.after);
  }
}
```

## API

### Types

- **`Operation`** — `"insert" | "update" | "delete" | "read" | "control"`
- **`ChangeEvent`** — interface matching the Go `event.ChangeEvent` json tags exactly

### Runtime helpers

| Function | Description |
|---|---|
| `isChangeEvent(obj)` | Type guard validating an unknown value as a `ChangeEvent` |
| `isInsert(ev)` | Narrows to insert operation |
| `isUpdate(ev)` | Narrows to update operation |
| `isDelete(ev)` | Narrows to delete operation |
| `isRead(ev)` | Narrows to snapshot read operation |
| `isControl(ev)` | Narrows to control/lifecycle operation |

## License

Apache-2.0
