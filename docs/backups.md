# Backups

Lex backs up its database (users, watch history, matches, collections and
settings) daily and before every upgrade that changes it, into
`<data>/backups` or the folder set in Settings → Backups, keeping the newest
seven of each kind. Settings → Backups lists them and can back up now or
download one. Artwork, subtitles and seek previews aren't backed up; Lex
rebuilds them.

To restore, stop Lex and run `lex -data <data> -restore <file>`; the current
database is backed up first. With the container image:

```sh
docker run --rm -v lex-data:/data ghcr.io/mynamearnav/lex:latest \
  -restore /data/backups/<file>
```
