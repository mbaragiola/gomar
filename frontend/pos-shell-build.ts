import { createHash } from 'node:crypto'
import type { Plugin } from 'vite'
import { themeBootstrap } from './theme-bootstrap'

export function posShell(): Plugin {
  return {
    name: 'gomar-pos-static-shell',
    apply: 'build',
    generateBundle(_, bundle) {
      const script = bundle['app.js']
      const style = bundle['style.css']
      if (script?.type !== 'chunk' || style?.type !== 'asset') this.error('Missing POS shell assets.')
      const version = createHash('sha256').update(script.code).update(style.source).update(themeBootstrap).digest('hex')
      const assets = ['/static/pos-shell.html', `/static/app.js?pos=${version}`, `/static/style.css?pos=${version}`]
      this.emitFile({ type: 'asset', fileName: 'pos-shell.html', source: `<!doctype html>
<html lang="es-AR">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Gomar · Caja</title>
<script>${themeBootstrap}</script>
<link rel="stylesheet" href="${assets[2]}">
</head>
<body><div id="app"></div><script type="module" src="${assets[1]}"></script></body>
</html>` })
      this.emitFile({ type: 'asset', fileName: 'pos-sw.js', source: `
'use strict';
const version = ${JSON.stringify(version)};
const name = 'gomar-pos-shell-' + version;
const paths = ${JSON.stringify(assets)};
const urls = paths.map(p => new URL(p, self.location.origin).href);
self.addEventListener('install', event => {
  event.waitUntil((async () => {
    const cache = await caches.open(name);
    await cache.addAll(urls.map(url => new Request(url, { credentials: 'omit', cache: 'reload', redirect: 'error' })));
    await self.skipWaiting();
  })());
});
self.addEventListener('activate', event => {
  event.waitUntil((async () => {
    for (const key of await caches.keys()) {
      if (key.startsWith('gomar-pos-shell-') && key !== name) await caches.delete(key);
    }
    await self.clients.claim();
  })());
});
self.addEventListener('fetch', event => {
  const req = event.request;
  if (req.method !== 'GET') return;
  if (req.mode === 'navigate' && new URL(req.url).origin === self.location.origin
      && new URL(req.url).pathname === '/pos') {
    event.respondWith(fetch(req).catch(async () =>
      (await (await caches.open(name)).match(urls[0])) || Response.error()));
    return;
  }
  if (req.url !== urls[1] && req.url !== urls[2]) return;
  event.respondWith((async () =>
    (await (await caches.open(name)).match(req.url)) || fetch(req))());
});
` })
    },
  }
}
