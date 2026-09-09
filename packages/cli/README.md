# @ocel/cli

The `ocel` command line, deploying apps into your own cloud.

```sh
npm install -g @ocel/cli
ocel init --provider aws
ocel deploy
```

The binary itself ships in `@ocel/cli-<os>-<arch>` — one optional dependency per platform,
so a package manager downloads only yours. Targets are `darwin-arm64`, `darwin-x64`,
`linux-arm64`, `linux-x64` and `win32-x64`.

Two other ways to install it:

```sh
curl -fsSL https://ocel.dev/install.sh | sh
brew install ocelhq/tap/ocel
```

The `ocel` package is the SDK your app imports; it no longer carries the binary.
