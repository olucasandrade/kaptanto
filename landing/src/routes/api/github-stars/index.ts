import type { RequestHandler } from "@builder.io/qwik-city";

const REPO_URL = "https://api.github.com/repos/olucasandrade/kaptanto";
/** Shown if GitHub is unreachable. Update occasionally; live count wins. */
const FALLBACK_STARS = 64;

export const onGet: RequestHandler = async ({ json, cacheControl }) => {
  cacheControl({
    public: true,
    maxAge: 60,
    sMaxAge: 3600,
    staleWhileRevalidate: 86400,
  });

  try {
    const res = await fetch(REPO_URL, {
      headers: {
        Accept: "application/vnd.github+json",
        "User-Agent": "kaptanto-landing",
      },
    });
    if (!res.ok) {
      json(200, { stars: FALLBACK_STARS });
      return;
    }
    const data = (await res.json()) as { stargazers_count?: unknown };
    const n = data.stargazers_count;
    json(200, {
      stars: typeof n === "number" && Number.isFinite(n) ? n : FALLBACK_STARS,
    });
  } catch {
    json(200, { stars: FALLBACK_STARS });
  }
};
