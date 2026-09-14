<#
.SYNOPSIS
  Recolhe para a maquina do OPERADOR os backups cifrados do no `aos`, e VERIFICA A RECENCIA.

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
  RECENCIA, e porque nao basta recolher.

  A versao anterior era idempotente por NOME e terminava com "FEITO — 0 novo(s)". Se o cron do
  servidor morresse, ela continuaria a dizer exactamente isso, para sempre, com codigo de saida
  zero. Um SUCESSO VACUOSO: a mensagem de um sistema saudavel e a de um sistema que parou de
  produzir backups ha semanas sao a MESMA.

  Por isso este script verifica a idade dos DOIS lados:

    - a copia LOCAL mais recente  -> apanha "a minha maquina esteve dias desligada"
    - o backup REMOTO mais recente -> apanha "o cron do servidor morreu", que e o caso que
                                      NINGUEM notaria, porque a recolha continua a correr bem

  Um alerta sai por tres canais, de propósito redundantes porque nenhum deles e garantido:
  codigo de saida != 0 (visivel no "Last Run Result" do Agendador de Tarefas), o ficheiro
  ESTADO.txt no destino, e o log. O Registo de Eventos e tentado e degrada em silencio se a
  tarefa nao correr elevada.

  O QUE ISTO NAO RESOLVE: a maquina desligada nao alerta enquanto esta desligada — nenhum
  processo local pode. O que deixa de existir e a copia velha SILENCIOSA com a maquina ligada.

  A CHAVE SO RECOLHE. A tarefa corre sozinha, pelo que a chave nao tem passphrase — e o `aos` esta
  no grupo docker, onde uma shell e root no servidor. Por isso a chave e DEDICADA
  (secrets-local/backup-pull/) e o servidor forca-lhe um comando (backup-pull-gate.sh) que so
  aceita tres pedidos: `listar`, `recente` e `scp -f <um artefacto do backup.sh>`. O `scp` vai com
  -O (protocolo classico): por SFTP, que e o default do OpenSSH 9, o pedido nao traria um caminho
  que o gate pudesse validar. Ver deploy/server/README.md seccao "Backup".
#>
[CmdletBinding()]
param(
    [string]$Destino  = "$env:USERPROFILE\aos-backups",
    [string]$Chave    = "C:\Jimy\AOS\deploy\server\secrets-local\backup-pull\id_ed25519",
    [string]$Servidor = "aos@37.60.241.150",
    [int]$Porta       = 22,
    # Vazio = o known_hosts do utilizador. So existe para o ensaio contra um sshd descartavel.
    [string]$KnownHosts = "",
    # Quantas copias manter AQUI. O servidor tem a sua propria rotacao (14); esta e independente
    # e pode ser mais generosa, porque e a unica que sobrevive a perda da maquina remota.
    [int]$Manter      = 30,
    # Tecto de idade. 48h e o dobro da cadencia diaria: um dia falhado nao alerta, dois sim.
    [int]$MaxIdadeHoras = 48
)

# BatchMode: a tarefa nunca pode ficar parada a pedir uma password. StrictHostKeyChecking fica no
# default (ask, que em BatchMode e recusa): um servidor com outra chave de host nao recebe pedidos.
$sshOpts = @('-i', $Chave, '-o', 'IdentitiesOnly=yes', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=20')
if ($KnownHosts) { $sshOpts += @('-o', "UserKnownHostsFile=$KnownHosts") }

$ErrorActionPreference = 'Stop'
$log     = Join-Path $Destino 'pull.log'
$estado  = Join-Path $Destino 'ESTADO.txt'
$alertas = @()

# Nativo corre um executavel externo com o tratamento de erro RELAXADO, e existe por causa de um
# defeito que so aparecia no pior momento: com $ErrorActionPreference='Stop', o PowerShell trata
# QUALQUER escrita para stderr de um executavel nativo como erro TERMINANTE. O `ssh` escreve
# "connect to host ... timed out" para stderr, portanto com o servidor inalcancavel o script
# MORRIA aqui — sem alerta, sem ESTADO.txt, com codigo de saida 1 de crash em vez do 2 do
# caminho tratado. O unico cenario em que o aviso interessa era o unico em que nao saia.
function Nativo([scriptblock]$bloco) {
    $anterior = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try { & $bloco } finally { $ErrorActionPreference = $anterior }
}

function Escreve($msg) {
    $linha = "{0}  {1}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $msg
    Write-Output $linha
    if (Test-Path $Destino) { Add-Content -Path $log -Value $linha -Encoding utf8 }
}
function Alerta($msg) {
    $script:alertas += $msg
    Escreve "ALERTA: $msg"
}
# Fecha SEMPRE pelo mesmo sitio: o ESTADO.txt e o codigo de saida contam a mesma historia, e
# quem olhar para um nao precisa de ir procurar o outro.
function Termina([int]$codigo) {
    $ok = ($script:alertas.Count -eq 0)
    $txt = @(
        ("AOS backups — {0}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss')),
        ("estado: " + $(if ($ok) { "OK" } else { "ALERTA" })),
        ""
    ) + $(if ($ok) { @("Nada a assinalar.") } else { $script:alertas | ForEach-Object { "  - $_" } })
    if (Test-Path $Destino) { Set-Content -Path $estado -Value $txt -Encoding utf8 }
    if (-not $ok) {
        # Canal de melhor-esforco: sem elevacao, New-EventLog falha e nao ha nada a fazer quanto
        # a isso — os outros dois canais nao dependem de privilegio.
        try {
            if (-not [Diagnostics.EventLog]::SourceExists('AOS-Backups')) {
                New-EventLog -LogName Application -Source 'AOS-Backups' -ErrorAction Stop
            }
            Write-EventLog -LogName Application -Source 'AOS-Backups' -EntryType Warning `
                -EventId 1001 -Message ($script:alertas -join "`n") -ErrorAction Stop
        } catch { Escreve "  (Registo de Eventos indisponivel: $($_.Exception.Message))" }
    }
    exit $codigo
}

if (-not (Test-Path $Destino)) { New-Item -ItemType Directory -Path $Destino -Force | Out-Null }
if (-not (Test-Path $Chave))   { Alerta "chave SSH ausente em $Chave"; Termina 1 }

# Lista remota COM data de modificacao. Falha de rede NAO e fatal para o agendamento: a execucao
# seguinte tenta de novo — derrubar a tarefa por um servidor momentaneamente inalcancavel trocaria
# uma copia em atraso por nenhuma copia.
$remotos = Nativo { & ssh @sshOpts -p $Porta $Servidor 'listar' 2>$null }
if ($LASTEXITCODE -ne 0 -or -not $remotos) {
    Alerta "servidor inalcancavel ou sem backups nenhuns — a proxima execucao tenta de novo"
    Termina 2
}

# IDADE DO LADO REMOTO. E a verificacao que a versao anterior nao tinha, e a que apanha o caso
# invisivel: o cron morreu, a recolha continua a correr sem erro, e o unico sintoma seria a data
# do ficheiro mais recente — que ninguem estava a olhar.
$epochRemoto = Nativo { & ssh @sshOpts -p $Porta $Servidor 'recente' 2>$null }
if ($LASTEXITCODE -eq 0 -and $epochRemoto) {
    $dataRemota  = [DateTimeOffset]::FromUnixTimeSeconds([int64]$epochRemoto).LocalDateTime
    $idadeRemota = [int]((Get-Date) - $dataRemota).TotalHours
    Escreve ("backup mais recente NO SERVIDOR: {0:yyyy-MM-dd HH:mm} ({1}h)" -f $dataRemota, $idadeRemota)
    if ($idadeRemota -gt $MaxIdadeHoras) {
        Alerta ("o SERVIDOR nao produz backups ha {0}h (tecto {1}h) — o cron das 03:17 provavelmente morreu. A recolha continua a 'funcionar' e nao ha nada de novo para recolher." -f $idadeRemota, $MaxIdadeHoras)
    }
} else {
    Alerta "nao consegui datar o backup remoto — a verificacao de recencia do servidor NAO correu"
}

$novos = 0
foreach ($r in $remotos) {
    $nome = Split-Path $r -Leaf
    # O nome vem do servidor e vai dar a um caminho LOCAL: so passa a forma exacta de um artefacto do
    # backup.sh. Um servidor comprometido nao escolhe onde esta maquina escreve.
    if ($nome -notmatch '^aos-\d{8}T\d{6}Z\.tar\.gz\.enc$') { Alerta "nome inesperado na lista do servidor, ignorado: $nome"; continue }
    $alvo = Join-Path $Destino $nome
    if (Test-Path $alvo) { continue }
    # -O: protocolo classico. O gate do servidor so aceita `scp -f <caminho validado>`; por SFTP (o
    # default do OpenSSH 9) o pedido e recusado, de proposito.
    Nativo { & scp -O -q @sshOpts -P $Porta "${Servidor}:/opt/aos/backups/$nome" $alvo 2>$null }
    if ($LASTEXITCODE -ne 0) { Alerta "falhou a recolha de $nome"; continue }
    # VERIFICACAO: um ficheiro truncado a meio da transferencia seria indistinguivel de um backup
    # bom ate ao dia em que precisasse dele — que e o pior momento possivel para descobrir.
    $bytes = (Get-Item $alvo).Length
    if ($bytes -lt 1024) { Alerta "$nome so tem $bytes bytes — removido"; Remove-Item $alvo -Force; continue }
    Escreve "recolhido $nome ($bytes bytes)"
    $novos++
}

# Rotacao local, por data de escrita.
$copias = @(Get-ChildItem -Path $Destino -Filter '*.tar.gz.enc' | Sort-Object LastWriteTime -Descending)
if ($copias.Count -gt $Manter) {
    $copias | Select-Object -Skip $Manter | ForEach-Object {
        Remove-Item $_.FullName -Force
        Escreve "rotacao: removido $($_.Name)"
    }
}

# IDADE DO LADO LOCAL. Aqui a data de escrita e a da RECOLHA, nao a da producao — o que se mede e
# "ha quanto tempo esta maquina tem uma copia", que e exactamente a pergunta que interessa quando
# o servidor se perde.
if ($copias.Count -eq 0) {
    Alerta "NAO HA COPIA LOCAL NENHUMA — a perda do servidor seria a perda de tudo"
} else {
    $idadeLocal = [int]((Get-Date) - $copias[0].LastWriteTime).TotalHours
    Escreve ("copia local mais recente: {0:yyyy-MM-dd HH:mm} ({1}h)" -f $copias[0].LastWriteTime, $idadeLocal)
    if ($idadeLocal -gt $MaxIdadeHoras) {
        Alerta ("a copia local tem {0}h (tecto {1}h) — esta maquina esteve desligada, ou a recolha falha ha dias" -f $idadeLocal, $MaxIdadeHoras)
    }
}

Escreve ("FEITO — {0} novo(s); {1} copia(s) locais em {2}" -f $novos, [Math]::Min($copias.Count, $Manter), $Destino)
Termina $(if ($alertas.Count -gt 0) { 3 } else { 0 })
