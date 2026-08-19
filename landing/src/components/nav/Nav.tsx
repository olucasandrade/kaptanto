import { component$, useSignal, useVisibleTask$ } from "@builder.io/qwik";
import type { Signal } from "@builder.io/qwik";
import { useLocation } from "@builder.io/qwik-city";
import { isDocsPath } from "../../data/docs-html";

interface NavProps {
  currentDoc: Signal<string | null>;
}

export const Nav = component$<NavProps>(({ currentDoc }) => {
  const loc = useLocation();
  const docsRoute = isDocsPath(loc.url.pathname);
  const isMenuOpen = useSignal(false);
  const isLanding = !currentDoc.value;
  const stars = useSignal<string | null>(null);

  // eslint-disable-next-line qwik/no-use-visible-task
  useVisibleTask$(async () => {
    const cached = sessionStorage.getItem("ghStars");
    if (cached) {
      stars.value = cached;
      return;
    }
    try {
      const res = await fetch(
        "https://api.github.com/repos/olucasandrade/kaptanto",
      );
      if (res.ok) {
        const data = await res.json();
        const count = new Intl.NumberFormat("en").format(data.stargazers_count);
        stars.value = count;
        sessionStorage.setItem("ghStars", count);
      }
    } catch {
      // silently fail if rate-limited or offline
    }
  });

  return (
    <nav class="nav">
      <div class="ni">
        <a
          href="/"
          class="nb"
          onClick$={(e) => {
            if (docsRoute) return;
            e.preventDefault();
            currentDoc.value = null;
          }}
        >
          <img src="/logo.png" alt="Kaptanto logo" class="nlg" />
          kaptanto
        </a>
        <div class={`nl${isMenuOpen.value ? " open" : ""}`} id="navL">
          <a
            href="/"
            onClick$={(e) => {
              if (docsRoute) return;
              e.preventDefault();
              currentDoc.value = null;
              window.scrollTo(0, 0);
            }}
            data-p="landing"
            class={isLanding ? "a" : ""}
          >
            Home
          </a>
          <a
            href={docsRoute ? "/docs/docs-intro" : "/?doc=docs-intro"}
            onClick$={(e) => {
              if (docsRoute) return;
              e.preventDefault();
              currentDoc.value = "docs-intro";
              window.scrollTo(0, 0);
            }}
            data-p="docs"
            class={currentDoc.value ? "a" : ""}
          >
            Docs
          </a>
          <a
            href={docsRoute ? "/#features" : "#features"}
            onClick$={() => {
              if (docsRoute) return;
              currentDoc.value = null;
            }}
          >
            Features
          </a>
          <a
            href={docsRoute ? "/#compare" : "#compare"}
            onClick$={() => {
              if (docsRoute) return;
              currentDoc.value = null;
            }}
          >
            Compare
          </a>
          <a
            href={docsRoute ? "/#changelog" : "#changelog"}
            onClick$={() => {
              if (docsRoute) return;
              currentDoc.value = null;
            }}
          >
            Changelog
          </a>
          <a
            href={docsRoute ? "/docs/docs-benchmarks" : "/?doc=docs-benchmarks"}
            onClick$={(e) => {
              if (docsRoute) return;
              e.preventDefault();
              currentDoc.value = "docs-benchmarks";
              window.scrollTo(0, 0);
            }}
            data-p="docs"
          >
            Benchmarks
          </a>
          <a
            href="https://github.com/olucasandrade/kaptanto"
            class="ng"
            target="_blank"
            rel="noopener"
          >
            <svg width="13" height="13" viewBox="0 0 24 24" fill="currentColor">
              <path d="M12 0C5.37 0 0 5.37 0 12c0 5.31 3.435 9.795 8.205 11.385.6.105.825-.255.825-.57 0-.285-.015-1.23-.015-2.235-3.015.555-3.795-.735-4.035-1.41-.135-.345-.72-1.41-1.23-1.695-.42-.225-1.02-.78-.015-.795.945-.015 1.62.87 1.845 1.23 1.08 1.815 2.805 1.305 3.495.99.105-.78.42-1.305.765-1.605-2.67-.3-5.46-1.335-5.46-5.925 0-1.305.465-2.385 1.23-3.225-.12-.3-.54-1.53.12-3.18 0 0 1.005-.315 3.3 1.23.96-.27 1.98-.405 3-.405s2.04.135 3 .405c2.295-1.56 3.3-1.23 3.3-1.23.66 1.65.24 2.88.12 3.18.765.84 1.23 1.905 1.23 3.225 0 4.605-2.805 5.625-5.475 5.925.435.375.81 1.095.81 2.22 0 1.605-.015 2.895-.015 3.3 0 .315.225.69.825.57A12.02 12.02 0 0024 12c0-6.63-5.37-12-12-12z" />
            </svg>
            GitHub
            <span class="ns" id="ghStars">
              {stars.value}
            </span>
          </a>
        </div>
        <button
          class="nmob"
          onClick$={() => {
            isMenuOpen.value = !isMenuOpen.value;
          }}
          aria-label="Menu"
        >
          <svg
            width="18"
            height="18"
            fill="none"
            stroke="currentColor"
            stroke-width="1.5"
          >
            <path d="M3 5h18M3 11h18M3 17h18" />
          </svg>
        </button>
      </div>
    </nav>
  );
});
