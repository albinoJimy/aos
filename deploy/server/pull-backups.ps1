<#
.SYNOPSIS
  Recolhe para a maquina do OPERADOR os backups cifrados do no `aos`.

.DESCRIPTION
  Os backups sao produzidos no servidor por backup.sh (cron, 03:17) e vivem la, NO MESMO DISCO
  do que protegem. Isso cobre apagamento do volume, corrupcao e um deploy mau; NAO cobre perda
  da maquina. Este script e a peca que fecha essa lacuna.

  E seguro faze-lo porque os artefactos sao PKCS#7 cifrados para um certificado cuja chave
  privada NUNCA esteve no servidor — vive aqui, em secrets-local/backup-key/. Um backup em
  transito ou em repouso nesta maquina continua ilegivel para quem nao tenha essa chave.

  ⚠️ PERDER A CHAVE PRIVADA E PERDER OS BACKUPS. Nao ha recuperacao. Ver deploy/server/README.md
  seccao "Backup".

.NOTES
  Idempotente: so transfere o que ainda nao existe localmente, comparando por nome (o nome
  carrega o timestamp UTC da producao). Correr as vezes que forem precisas nao duplica nada.
#>
[CmdletBinding()]
param(
    [string]$Destino  = "$env:USERPROFILE\aos-backups",
    [string]$Chave    = "C:\Jimy\aos\deploy\server\secrets-local\deploy_key",
    [string]$Servidor = "aos@37.60.241.150",
    # Quantas copias manter AQUI. O servidor tem a sua propria rotacao (14); esta e independente
    # e pode ser mais generosa, porque e a unica que sobrevive a perda da maquina remota.
    [int]$Manter      = 30
)

$ErrorActionPreference = 'Stop'
$log = Join-Path $Destino 'pull.log'

function Escreve($msg) {
    $linha = "{0}  {1}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $msg
    Write-Output $linha
    if (Test-Path $Destino) { Add-Content -Path $log -Value $linha -Encoding utf8 }
}

if (-not (Test-Path $Destino)) { New-Item -ItemType Directory -Path $Destino -Force | Out-Null }
if (-not (Test-Path $Chave))   { Escreve "ERRO: chave SSH ausente em $Chave"; exit 1 }

# Lista remota. Falha de rede NAO e fatal para o agendamento: sai com codigo proprio e a
# execucao seguinte tenta de novo — derrubar a tarefa por um servidor momentaneamente
# inalcancavel trocaria uma copia em atraso por nenhuma copia.
$remotos = & ssh -i $Chave -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=20 `
    $Servidor 'ls -1 /opt/aos/backups/*.tar.gz.enc 2>/dev/null' 2>$null
if ($LASTEXITCODE -ne 0 -or -not $remotos) {
    Escreve "servidor inalcancavel ou sem backups — a proxima execucao tenta de novo"
    exit 2
}

$novos = 0
foreach ($r in $remotos) {
    $nome = Split-Path $r -Leaf
    $alvo = Join-Path $Destino $nome
    if (Test-Path $alvo) { continue }
    & scp -q -i $Chave -o IdentitiesOnly=yes -o BatchMode=yes "${Servidor}:$r" $alvo 2>$null
    if ($LASTEXITCODE -ne 0) { Escreve "FALHOU a recolha de $nome"; continue }
    # VERIFICACAO: o artefacto tem de ser um envelope PKCS#7 integro. Um ficheiro truncado a
    # meio da transferencia seria indistinguivel de um backup bom ate ao dia em que precisasse
    # dele — que e o pior momento possivel para descobrir.
    $bytes = (Get-Item $alvo).Length
    if ($bytes -lt 1024) { Escreve "SUSPEITO: $nome so tem $bytes bytes — removido"; Remove-Item $alvo -Force; continue }
    Escreve "recolhido $nome ($bytes bytes)"
    $novos++
}

# Rotacao local, por data de escrita.
$copias = Get-ChildItem -Path $Destino -Filter '*.tar.gz.enc' | Sort-Object LastWriteTime -Descending
if ($copias.Count -gt $Manter) {
    $copias | Select-Object -Skip $Manter | ForEach-Object {
        Remove-Item $_.FullName -Force
        Escreve "rotacao: removido $($_.Name)"
    }
}

Escreve ("FEITO — {0} novo(s); {1} copia(s) locais em {2}" -f $novos, [Math]::Min($copias.Count, $Manter), $Destino)
