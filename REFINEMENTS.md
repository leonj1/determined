# REFINEMENTS: Theme Toggle Plan Review

- **Step 2 `Done when:` circularly depends on Step 5.** The criterion states "The Go contract test in Step 5 verifies…" making Step 2 unverifiable at implementation time. Replace with a check performable at Step 2 (e.g., inspect `view-source:` of the served page and confirm `"determined-theme"` appears before `<style>` in the raw HTML).

- **Step 4 does not specify first-click behavior from a non-auto starting state.** The cycling order `auto → light → dark → auto` is stated, but when the page loads with a forced theme from `localStorage` (e.g., `"dark"`), the implementer must guess whether the first click transitions to `auto` or to `light`. This is a consequential UX design choice that should be settled in the step text.

- **No error-handling specification for `localStorage` unavailability (affects Steps 2 and 4).** Both the anti-flash script (Step 2) and the toggle logic (Step 4) call `localStorage.getItem`, `setItem`, and `removeItem` directly. The plan is silent on behavior when `localStorage` throws (privacy-restricted contexts, Safari private browsing, storage-full conditions). The implementer must guess whether to silently degrade to `auto`, wrap in try/catch, or let the script fail.
