# Bootstrap — estado remoto local

O backend S3 precisa de um bucket **antes** do primeiro `init`. Este bootstrap sobe
um MinIO (S3-compatível) e cria o bucket `aos-tfstate` com versioning — para que o
locking nativo (`use_lockfile`) e o estado remoto funcionem offline, sem uma conta cloud.

## Arranque

```bash
docker compose -f infra/bootstrap/docker-compose.yml up -d
```

Depois, exporta as credenciais (dev-local, **não são segredos de produção**) e inicializa:

```bash
export AWS_ACCESS_KEY_ID=aos-dev-minio
export AWS_SECRET_ACCESS_KEY=aos-dev-minio-pass
cd infra && tofu init -backend-config=backend-dev.hcl
```

- Consola MinIO: <http://localhost:9001> (utilizador/pass acima).
- Para **staging real**, ignora este bootstrap e aponta `backend-staging.hcl` para um
  bucket S3/GCS gerido (com versioning ativado), fornecendo credenciais por ambiente.

## Encerramento

```bash
docker compose -f infra/bootstrap/docker-compose.yml down          # mantém o estado
docker compose -f infra/bootstrap/docker-compose.yml down -v       # apaga o estado (destrutivo)
```
