# Vault de PRODUÇÃO do nó `aos` — custódia da KEK por-titular (AOS-215/AOS-216), motor Transit.
#
# PORQUE EXISTE. O nó cifra o conteúdo não-determinístico dos runs (texto do modelo, resultados
# de tools) com uma KEK POR-TITULAR, antes de tocar o Event Store. Sem custódia externa essa KEK
# vive no vault in-memory de referência — e um restart torna tudo o que foi selado
# PERMANENTEMENTE indecifrável. Não é perda de cache: é apagamento silencioso de dados que o
# legal hold promete preservar. É por isso que `AOS_MODE=production` recusa arrancar sem isto.
#
# PORQUE NÃO `-dev`. O modo -dev guarda tudo em memória e faz auto-unseal — o Transit e as KEKs
# evaporam a cada restart, o que reintroduz exactamente o problema que este serviço resolve.
# Storage `file` num volume nomeado faz o material SOBREVIVER. O custo é init/unseal reais.

# /vault/file: directório que a imagem oficial já cria com dono vault:vault. Montar o volume
# nomeado AQUI faz o Docker herdar essa posse — um volume em /vault/data ficaria root-owned e o
# Vault, que corre como uid 100, não conseguiria escrever ("permission denied" no init).
storage "file" {
  path = "/vault/file"
}

listener "tcp" {
  address       = "0.0.0.0:8200"
  # TLS OBRIGATÓRIO, e não por gosto: o nó recusa `http` para o Vault fora de loopback — o mesmo
  # critério que aplica ao verificador de attestation remoto. Certificado com SAN=vault, assinado
  # pela CA INTERNA cuja privada vive na máquina do operador, nunca aqui.
  tls_cert_file = "/vault/tls/vault.crt"
  tls_key_file  = "/vault/tls/vault.key"
  tls_min_version = "tls12"
}

# mlock impede que material de chave desça para swap. Exige CAP_IPC_LOCK, que o compose concede
# (`cap_add: IPC_LOCK`). Neste host o swap está a 0 B, pelo que a protecção é hoje redundante —
# mas o host pode ganhar swap sem que ninguém se lembre desta linha, e então deixa de ser.
disable_mlock = false

# Sem UI: o Vault não publica porta no host e só o nó lhe fala. Uma consola que ninguém alcança é
# superfície sem utilizador.
ui = false
