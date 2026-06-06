# Pretty install URL via the domain

The one-liners use `https://yutugyutugyutug.com/install` (and `/install.ps1`). The domain just
**redirects** to the raw install scripts on GitHub — `curl -L` / `irm` follow the redirect, so
nothing else is needed.

## Cloudflare setup (Redirect Rules — 2 minutes, free)

Dashboard → your domain → **Rules → Redirect Rules → Create rule**, add these two:

**1. install (shell)**
- When incoming requests match: **URI Path** `equals` `/install`
- Then: **Static** redirect → URL
  `https://raw.githubusercontent.com/aasm3535/lobster/main/install.sh`
- Status code: **302**

**2. install (PowerShell)**
- When incoming requests match: **URI Path** `equals` `/install.ps1`
- Then: **Static** redirect → URL
  `https://raw.githubusercontent.com/aasm3535/lobster/main/install.ps1`
- Status code: **302**

That's it. Test:

```sh
curl -fsSL https://yutugyutugyutug.com/install | head
```

## Alternative: a Cloudflare Worker (serves the script inline, no redirect)

Create a Worker bound to `yutugyutugyutug.com/install*` with:

```js
export default {
  async fetch(req) {
    const p = new URL(req.url).pathname
    const file = p === '/install.ps1' ? 'install.ps1' : 'install.sh'
    const r = await fetch(`https://raw.githubusercontent.com/aasm3535/lobster/main/${file}`)
    return new Response(r.body, { headers: { 'content-type': 'text/plain; charset=utf-8' } })
  },
}
```

## Releases

The installer downloads the binary for the user's platform from the latest GitHub release.
Cut one so there's something to download:

```sh
git tag v0.1.0 && git push origin v0.1.0
```

The `release` workflow then cross-compiles and attaches the binaries automatically.
