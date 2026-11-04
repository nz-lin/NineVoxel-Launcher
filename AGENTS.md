# Repository Guidelines

## Project Structure & Module Organization

NineVoxel Launcher is a Minecraft Java Edition desktop launcher built with Wails v3, Go, React, TypeScript, and Tailwind CSS. Root Go files provide application entry points; `internal/` contains backend packages such as `account`, `download`, `launch`, `runtime`, and `settings`. SQLite migrations live in `internal/database/migrations/`.

Frontend code lives in `frontend/src/`, organized into `components/`, `hooks/`, `services/`, `types/`, and `assets/`. Backend tests sit beside implementation files. `build/` holds platform packaging configuration; `scripts/` contains development helpers. Generated `frontend/bindings/`, `frontend/dist/`, and `bin/` are ignored; do not commit them.

## Build, Test, and Development Commands

Use the Go toolchain in `go.mod`, Node from `.node-version`, and pnpm matching `.github/workflows/ci.yml`.

- `pnpm --dir frontend install --frozen-lockfile`: install locked frontend dependencies.
- `wails3 generate bindings -clean=true -ts -i`: generate TypeScript bindings before frontend checks or builds.
- `wails3 dev`: run the desktop application with Vite hot reload.
- `pnpm --dir frontend run lint`: check TypeScript with `tsc --noEmit`.
- `pnpm --dir frontend run build`: build frontend assets.
- `go vet ./...` and `go test ./...`: run Go static checks and tests.
- `wails3 task build` / `wails3 task package`: build the application or platform installer.

Build frontend assets before root Go checks: `main.go` embeds `frontend/dist`, which is absent in a clean checkout.

## Core Storage

The shared game core lives under `artifact.DataRoot()`:

- `libraries/`, `assets/`, `versions/` and `core-index/` are shared by every instance.
- Instance folders contain only mutable state (mods, saves, config, logs).
- `versions/<id>/.nv-core-manifest.json` records every required file with size and SHA-256.
  It is the single source of truth for "is this version complete".
- `internal/core` owns all core writes. Do not delete or overwrite core files from
  `instance`, `launch` or `loader`.

Recovery paths:

- **Repair** (`instance.RepairInstance`) re-downloads only missing or corrupt files.
  Loader-processor outputs and version metadata have no independent download source,
  so damage there asks the user to reinstall.
- **Rebuild** (`instance.RebuildCoreForUser`) re-creates a clean core from the
  manifests of installed instances and switches atomically per directory. It first
  verifies and repairs each version. The switch is journaled; `core.RecoverRebuild`
  runs at startup and rolls back incomplete transactions.

Loader installation (`internal/loader`):

- Fabric/Quilt: the official metadata is a ready-to-launch version JSON.
- Forge/NeoForge: the official installer's `install_profile.json` is parsed and its
  bundled processor jars are executed in order (`loader.PlanForgeInstall` +
  `loader.RunForgeProcessors`). The installer's `--installClient` CLI entry point is
  deliberately not used: it is an implementation detail upstream may remove.
- Processor jars are cached under `cache/processors/` and are not part of any
  manifest; the produced client jar is recorded in the `processor` group.

Known limitations:

- Locks are process-local; do not point multiple launcher processes at one DataRoot.
- There is no cross-platform sharing: natives and recipe fingerprints are platform-specific.
- Automatic file-level garbage collection is not implemented; rebuild removes
  unreferenced files as a side effect, but normal deletion keeps unused libraries/assets.
- Rebuild copies an entire core while the old one still exists, so it needs roughly
  one extra core's worth of free disk space.

## Coding Style & Naming Conventions

Format Go with `gofmt`; use lowercase package names and exported `PascalCase` identifiers. Use two-space indentation for TypeScript/TSX, `PascalCase` component filenames, and `useCamelCase` hook names. Follow surrounding quote and semicolon conventions. Frontend `lint` performs type checking; no ESLint or Prettier configuration is present.

### Comments

Write comment prose in Chinese; keep identifiers, commands, and API names in English. Explain intent, constraints, and the reason behind a non-obvious choice instead of restating the code.

- Go packages that own real behavior ship a `doc.go` whose package comment opens with `// Package <name> 提供…`. Group the following paragraphs under `#` headings covering design goals, hard constraints (especially security boundaries such as "credentials never reach the frontend"), relationships to sibling packages, and explicit non-goals. Exported identifiers get a comment that starts with the identifier's own name.
- Frontend components, hooks, and services open with a multi-line `/** */` block describing the module's role. Document member-level intent with a single-line `/** … */` on state variables, derived values, and helpers, and record the rule's source when there is one, for example `/** 离线玩家名规则：3-16 位字母、数字或下划线（与后端离线来源的输入校验一致）。 */`.
- Group long files with `// ===== 主题 =====` separators and cite the decision record or task id when a block implements one.
- Skip comments that merely restate a signature or a mechanical statement, and update or delete comments that no longer match behavior.

## Testing Guidelines

Use Go's standard `testing` package, `*_test.go` filenames, and `TestXxx` functions; table-driven cases are common. Run focused checks such as `go test ./internal/runtime` during development, then the full suite. No coverage threshold or frontend test runner is configured. Desktop windows, frontend/backend events, and real interactions require manual acceptance testing.

## Commit & Pull Request Guidelines

Follow the existing Conventional Commits style: `feat(runtime): ...`, `fix(frontend): ...`, or `docs: ...`; Chinese descriptions are common. Keep changes focused. Pull requests should describe behavior changes, link relevant issues, record validation commands and results, and include screenshots for UI changes. Identify any manual checks still required.
