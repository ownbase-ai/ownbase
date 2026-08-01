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
  { ignores: ["dist", "src-tauri/target"] },
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
  {
    files: ["**/*.{js,mjs}"],
    extends: [js.configs.recommended],
    languageOptions: { globals: globals.node },
  },
);
