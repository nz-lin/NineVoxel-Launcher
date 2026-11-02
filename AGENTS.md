# Repository Guidelines

## Project Structure & Module Organization

NineVoxel Launcher is a Minecraft Java Edition desktop launcher built with Wails v3, Go, React, TypeScript, and Tailwind CSS. Root Go files provide application entry points; `internal/` contains backend packages such as `account`, `download`, `launch`, `runtime`, and `settings`. SQLite migrations live in `internal/database/migrations/`.

Frontend code lives in `frontend/src/`, organized into `components/`, `hooks/`, `services/`, `types/`, and `assets/`. Backend tests sit beside implementation files. `build/` holds platform packaging configuration; `scripts/` contains development helpers. Generated `frontend/bindings/`, `frontend/dist/`, and `bin/` are ignored; do not commit them.

## Build, Test, and Development Commands

Use the Go toolchain in `go.mod`, Node from `.node-version`, and pnpm matching `.gitlab-ci.yml`.

- `pnpm --dir frontend install --frozen-lockfile`: install locked frontend dependencies.
- `wails3 generate bindings -clean=true -ts -i`: generate TypeScript bindings before frontend checks or builds.
- `wails3 dev`: run the desktop application with Vite hot reload.
- `pnpm --dir frontend run lint`: check TypeScript with `tsc --noEmit`.
- `pnpm --dir frontend run build`: build frontend assets.
- `go vet ./...` and `go test ./...`: run Go static checks and tests.
- `wails3 task build` / `wails3 task package`: build the application or platform installer.

Build frontend assets before root Go checks: `main.go` embeds `frontend/dist`, which is absent in a clean checkout.

## Coding Style & Naming Conventions

Format Go with `gofmt`; use lowercase package names and exported `PascalCase` identifiers. Use two-space indentation for TypeScript/TSX, `PascalCase` component filenames, and `useCamelCase` hook names. Follow surrounding quote and semicolon conventions. Frontend `lint` performs type checking; no ESLint or Prettier configuration is present.

## Testing Guidelines

Use Go's standard `testing` package, `*_test.go` filenames, and `TestXxx` functions; table-driven cases are common. Run focused checks such as `go test ./internal/runtime` during development, then the full suite. No coverage threshold or frontend test runner is configured. Desktop windows, frontend/backend events, and real interactions require manual acceptance testing.

## Commit & Pull Request Guidelines

Follow the existing Conventional Commits style: `feat(runtime): ...`, `fix(frontend): ...`, or `docs: ...`; Chinese descriptions are common. Keep changes focused. Pull requests should describe behavior changes, link relevant issues, record validation commands and results, and include screenshots for UI changes. Identify any manual checks still required.
