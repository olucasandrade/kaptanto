import type { RequestHandler } from "@builder.io/qwik-city";

const REPO_URL = "https://api.github.com/repos/olucasandrade/kaptanto";
/** Shown if GitHub is unreachable. Update occasionally; live count wins. */
export const FALLBACK_STARS = 64;
export const FETCH_TIMEOUT_MS = 4000;

export async function loadStarCount(
  fetcher: typeof fetch = fetch,
): Promise<number> {
  const ac = new AbortController();
  const timer = setTimeout(() => ac.abort(), FETCH_TIMEOUT_MS);
  try {
    const res = await fetcher(REPO_URL, {
      signal: ac.signal,
      headers: {
        Accept: "application/vnd.github+json",
        "User-Agent": "kaptanto-landing",
      },
    });
    if (!res.ok) return FALLBACK_STARS;
    const data = (await res.json()) as { stargazers_count?: unknown };
    const n = data.stargazers_count;
    return typeof n === "number" && Number.isFinite(n) ? n : FALLBACK_STARS;
  } catch {
    return FALLBACK_STARS;
  } finally {
    clearTimeout(timer);
  }
}

export const onGet: RequestHandler = async ({ json, cacheControl }) => {
  cacheControl({
    public: true,
    maxAge: 60,
    sMaxAge: 3600,
    staleWhileRevalidate: 86400,
  });
  json(200, { stars: await loadStarCount() });
};
