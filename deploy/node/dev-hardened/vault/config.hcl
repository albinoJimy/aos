# Configuração do Vault para a stack dev-hardened — storage PERSISTENTE (não -dev).
#
# PORQUÊ não `-dev`: o modo -dev guarda tudo EM MEMÓRIA e auto-unseal — o motor Transit e as
# KEKs por-titular EVAPORAM em cada restart do container (ex.: reinício do Docker), partindo o
# crypto-shred. Com storage `file` num volume nomeado (vault-data), o Transit e as KEKs
# SOBREVIVEM. Custo: init/unseal REAIS (o -dev fazia-os por nós). O up-oidc.sh trata o
# init/unseal idempotente (1 share / threshold 1, material em secrets/, git-ignored).
#
# DEV vs PRODUÇÃO: aqui TLS termina na malha/rede interna (tls_disable) e o unseal usa um share
# guardado em ficheiro; produção usaria TLS no listener + AUTO-UNSEAL (KMS/HSM/transit) e uma
# cerimónia de chaves Shamir com múltiplos custodiantes.

# /vault/file: directório que a imagem oficial já cria com dono vault:vault. Montar o volume
# nomeado aqui faz o Docker herdar essa posse (um volume em /vault/data ficaria root-owned e o
# Vault, que corre como uid 100, não conseguiria escrever — "permission denied" no init).
storage "file" {
  path = "/vault/file"
}

listener "tcp" {
  address       = "0.0.0.0:8200"
  # TLS com cert SAN=vault assinado pela CA de dev (o nó/issuer confiam nela). Encripta o
  # transporte nó↔Vault (fecha DEF-012 nessa perna); produção usaria a PKI interna.
  tls_cert_file = "/vault/tls/tls.crt"
  tls_key_file  = "/vault/tls/tls.key"
}

# mlock exige CAP_IPC_LOCK; desligado em dev para evitar falhas de arranque conforme o host.
disable_mlock = true
ui            = true
