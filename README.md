# Originless

A single-container backend for content-addressed file storage over IPFS.

```bash
docker run -d --name originless -p 3232:3232 -e STORAGE_MAX=20GB ghcr.io/besoeasy/originless:latest
```

Open http://localhost:3232
