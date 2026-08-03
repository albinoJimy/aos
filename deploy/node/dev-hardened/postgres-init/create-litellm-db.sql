-- Cria a base de dados do LiteLLM (UI/virtual keys) na MESMA instância Postgres do Keycloak.
-- Corre UMA vez, no primeiro init do Postgres (volume vazio), via /docker-entrypoint-initdb.d.
-- Para um volume já inicializado, cria-a à mão: docker compose exec postgres createdb -U keycloak litellm
CREATE DATABASE litellm;
