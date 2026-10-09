# Portal branding

Set the dealership's name and optional logo and accent under `ddp` in `ddp.yaml`:

```yaml
ddp:
  name: acme
  display_name: Acme Dealership
  timezone: America/Chicago
  branding:
    logo: frontend/apps/portal/public/logo.svg
    accent: "#2457c5"
```

Keep the rest of the registry intact. Put the logo at the declared path before
building. Logos must be nonempty SVG, PNG, JPEG, WebP or GIF files of at most 1 MiB
under `frontend/apps/portal/public/`. Use ordinary files and directories; external
URLs, traversal paths and symlinks are refused. Everything in the public directory
is served without authentication, so it must contain only public assets.

The accent accepts six hexadecimal digits. The build derives readable light and
dark colors from it; displayed colors can differ from the input to preserve
contrast. The portal follows the browser's color-scheme preference. Omitting
`branding` keeps the DDP mark and green accent while using the configured display name.

The name, logo and colors appear on login, password recovery and the application
shell. The display name also sets the browser title. The System console uses
the same colors and retains its operator-only access policy.

Run `npm run build --prefix frontend` to validate and compile branding. `make build`
embeds that output into the binary; production image builds do the same. Rebuild
and deploy the image after branding changes. In development Vite reloads branding
edits; restart `ddp dev` after editing YAML so the API reads the same configuration.

`make check` verifies theme contrast and asset validation. `make check-smoke`
builds a branded synthetic project and checks its logo, login, table, account pages
and operator console in both color schemes against the real server.
