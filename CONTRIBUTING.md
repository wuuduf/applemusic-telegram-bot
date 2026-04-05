# Contributing

Thanks for considering a contribution.

## Before You Start
- Use Go `1.23+`.
- Make sure `wrapper` and media dependencies are set up if you test downloading flows.
- Never commit real secrets (`media-user-token`, `telegram-bot-token`, cookies, private API URLs).

## Development Setup
1. Copy `config.example.yaml` to `config.yaml`.
2. Fill in your own local values.
3. Run:
   ```bash
   gofmt -w main.go utils/**/*.go
   go test ./...
   go vet ./...
   ```

## Pull Request Guidelines
- Keep PRs focused and small when possible.
- Add or update docs when behavior changes.
- Include verification notes (what you ran, what passed).
- Preserve existing bot compatibility unless the change is explicitly breaking.

## Commit Hygiene
- Do not commit generated artifacts or caches.
- Do not commit credentials into config files or logs.
- Prefer clear commit messages that explain user-facing impact.

