import js from "@eslint/js";
import globals from "globals";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import tseslint from "typescript-eslint";

// The rule that earns its keep here is react-hooks/exhaustive-deps. Every screen
// in this app is a view onto a slow, side-effecting command, and a stale
// dependency list shows the user last Base's answer under this Base's name. The
// places that deliberately opt out are the ones that would otherwise re-run a
// `create` or reload on every render, and each is commented.
export default tseslint.config(
  { ignores: ["dist", "src-tauri/target", "playwright-report", "test-results"] },
  {
    files: ["**/*.{ts,tsx}"],
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    // A suppression that no longer suppresses anything is a comment claiming a
    // hazard that is gone.
    linterOptions: { reportUnusedDisableDirectives: "error" },
    languageOptions: {
      ecmaVersion: 2022,
      globals: globals.browser,
    },
    plugins: {
      "react-hooks": reactHooks,
      "react-refresh": reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      // Off deliberately. The rule is aimed at state that should have been
      // derived, and it is right about that — the router in App.tsx is derived
      // for exactly this reason. But this app's effects exist to read from an
      // external process: they run `ownbasectl` and put its answer into state,
      // which is the "subscribe to an external system" case the rule's own text
      // allows and its implementation cannot distinguish. Satisfying it would
      // mean adding a data-fetching library to a window that makes six kinds of
      // call, which is a dependency bought with nothing.
      "react-hooks/set-state-in-effect": "off",
      "react-refresh/only-export-components": [
        "warn",
        { allowConstantExport: true },
      ],
      // Unused arguments are how a React prop signature documents itself.
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_" },
      ],
    },
  },
  // Playwright specs and the IPC shim run in Node (or are serialized into the
  // browser). They share the TS rules above but need Node globals, not browser.
  {
    files: ["e2e/**/*.{ts,tsx}", "playwright.config.ts", "playwright.real.config.ts"],
    languageOptions: {
      globals: { ...globals.node, ...globals.browser },
    },
  },
  // Specs must use the shared test object so the shim fall-through guard runs
  // after every test. fixtures/test.ts, the shim, configs, and Tier-B specs
  // (tests-real/) may still import @playwright/test directly.
  {
    files: ["e2e/tests/**/*.{ts,tsx}"],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          paths: [
            {
              name: "@playwright/test",
              message:
                "Import { test, expect, waitForQuiet } from ../fixtures/test — that is what arms the shim fall-through guard.",
            },
          ],
        },
      ],
    },
  },
  {
    files: ["**/*.{js,mjs}"],
    extends: [js.configs.recommended],
    languageOptions: { globals: globals.node },
  },
);
