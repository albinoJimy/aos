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
  aceita quatro pedidos: `listar`, `recente`, `apagamentos` e `scp -f <um artefacto do backup.sh>`.
  O `scp` vai com -O (protocolo classico): por SFTP, que e o default do OpenSSH 9, o pedido nao
  traria um caminho que o gate pudesse validar. Ver deploy/server/README.md seccao "Backup".

  O REGISTO DE APAGAMENTOS (AOS-436). Cada bundle leva o Vault tal como estava — com as KEKs vivas
  nesse instante. Restaura-lo depois de um apagamento DSAR traz a KEK de volta. O backup.sh deixa,
  ao lado de cada bundle e EM CLARO, o registo de apagamentos do no: linhas `<id> <instante> <mac>`,
  com id e mac HMAC sob uma chave que so existe dentro do bundle cifrado — o ficheiro nao diz quem
  foi apagado e nao se forja sem ela. Este script guarda o MAIS RECENTE — e superconjunto de todos
  os anteriores — e verifica essa monotonia (por id) antes de largar o anterior. Nao verifica o MAC:
  a chave nao esta aqui, e e o no que o verifica ao importar. So serve para restaurar um bundle
  ANTERIOR ao ultimo (README seccao "Restaurar"); para o ultimo bundle nao acrescenta nada.
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
# ServerAlive: o ConnectTimeout so cobre o ESTABELECER da ligacao. Uma sessao que pendura depois de
# aberta (visto na primeira execucao agendada, 2026-09-14: um `recente` parado minutos numa ligacao
# instavel) ficava ate ao ExecutionTimeLimit da tarefa, sem alerta nenhum. Assim morre em ~60s, o
# script segue para o ramo de alerta, e a execucao seguinte tenta de novo.
$sshOpts = @('-i', $Chave, '-o', 'IdentitiesOnly=yes', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=20',
             '-o', 'ServerAliveInterval=15', '-o', 'ServerAliveCountMax=4')
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
# Ler-Registo le um registo de apagamentos e devolve id -> instante (o mais recente por id), ou
# $null se alguma linha COMPLETA estiver malformada. Estrito pela mesma razao que o no, e com as
# mesmas regras: -cnotmatch e [0-9] (sensivel a maiusculas, so digitos ASCII — o -notmatch e o \d
# do PowerShell aceitavam o que o no recusa), e o fragmento FINAL sem fim de linha e uma escrita
# interrompida, ignorada. O no escreve instantes RFC3339 UTC ('...Z'): a ordem de texto e a do tempo.
function Ler-Registo([string]$caminho) {
    $h = New-Object 'System.Collections.Generic.Dictionary[string,string]' ([StringComparer]::Ordinal)
    $texto = [IO.File]::ReadAllText($caminho, [Text.Encoding]::UTF8)
    $fim = $texto.LastIndexOf([char]10)
    $texto = if ($fim -ge 0) { $texto.Substring(0, $fim + 1) } else { '' }
    foreach ($l in ($texto -split "`n")) {
        $t = $l.Trim()
        if ($t -eq '' -or $t.StartsWith('#')) { continue }
        $c = $t -csplit '[ \t]+'
        if ($c.Count -ne 3 -or $c[0] -cnotmatch '^[0-9a-f]{64}$' -or $c[2] -cnotmatch '^[0-9a-f]{64}$' -or
            $c[1] -cnotmatch '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$') { return $null }
        if (-not $h.ContainsKey($c[0]) -or [string]::CompareOrdinal($c[1], $h[$c[0]]) -gt 0) { $h[$c[0]] = $c[1] }
    }
    # A virgula impede o PowerShell de desenrolar o dicionario em pares ao devolve-lo.
    return ,$h
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
# -n: stdin de /dev/null. Numa tarefa agendada nao ha consola, e um ssh a espera de stdin nao tem
# de quem o receber.
$remotos = Nativo { & ssh -n @sshOpts -p $Porta $Servidor 'listar' 2>$null }
if ($LASTEXITCODE -ne 0 -or -not $remotos) {
    Alerta "servidor inalcancavel ou sem backups nenhuns — a proxima execucao tenta de novo"
    Termina 2
}

# IDADE DO LADO REMOTO. E a verificacao que a versao anterior nao tinha, e a que apanha o caso
# invisivel: o cron morreu, a recolha continua a correr sem erro, e o unico sintoma seria a data
# do ficheiro mais recente — que ninguem estava a olhar.
$epochRemoto = Nativo { & ssh -n @sshOpts -p $Porta $Servidor 'recente' 2>$null }
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

# REGISTO DE APAGAMENTOS (AOS-436). So o mais recente interessa — e superconjunto de todos os
# anteriores —, mas so se larga o anterior depois de VERIFICAR que o novo o contem. Um registo que
# perdeu entradas e o sintoma de um volume restaurado sem importar o registo, ou de um no a escrever
# noutro sitio: nos dois casos, o proximo restauro ressuscitaria apagamentos, e isso tem de gritar.
$regRemoto = Nativo { & ssh -n @sshOpts -p $Porta $Servidor 'apagamentos' 2>$null }
if ($LASTEXITCODE -ne 0 -or -not $regRemoto) {
    Alerta "o servidor nao devolveu registo de apagamentos (AOS-436) - sem ele, restaurar um bundle ANTERIOR ao ultimo ressuscita as KEKs apagadas depois dele"
} else {
    $nomeReg = Split-Path ([string]($regRemoto | Select-Object -First 1)).Trim() -Leaf
    if ($nomeReg -cnotmatch '^apagamentos-[0-9]{8}T[0-9]{6}Z\.txt$') {
        Alerta "nome inesperado para o registo de apagamentos, ignorado: $nomeReg"
    } else {
        $alvoReg = Join-Path $Destino $nomeReg
        if (-not (Test-Path $alvoReg)) {
            Nativo { & scp -O -q @sshOpts -P $Porta "${Servidor}:/opt/aos/backups/$nomeReg" $alvoReg 2>$null }
            if ($LASTEXITCODE -ne 0 -or -not (Test-Path $alvoReg)) {
                Alerta "falhou a recolha do registo de apagamentos $nomeReg"
            } else {
                $novo = Ler-Registo $alvoReg
                if ($null -eq $novo) {
                    Alerta "o registo de apagamentos $nomeReg tem linhas malformadas - removido; o anterior fica"
                    Remove-Item $alvoReg -Force
                } else {
                    $anteriores = @(Get-ChildItem -Path $Destino -Filter 'apagamentos-*.txt' |
                        Where-Object { $_.Name -cne $nomeReg -and $_.Name -cmatch '^apagamentos-[0-9]{8}T[0-9]{6}Z\.txt$' } |
                        Sort-Object Name -Descending)
                    $perdidas = 0
                    if ($anteriores.Count -gt 0) {
                        $velho = Ler-Registo $anteriores[0].FullName
                        if ($null -ne $velho) {
                            foreach ($k in $velho.Keys) {
                                if (-not $novo.ContainsKey($k) -or [string]::CompareOrdinal($novo[$k], $velho[$k]) -lt 0) { $perdidas++ }
                            }
                        }
                    }
                    if ($perdidas -gt 0) {
                        Alerta ("o registo de apagamentos {0} NAO e superconjunto de {1}: {2} entrada(s) perdida(s). Os dois ficam guardados; investigar ANTES de qualquer restauro" -f $nomeReg, $anteriores[0].Name, $perdidas)
                    } else {
                        Escreve ("recolhido registo de apagamentos {0} ({1} entrada(s))" -f $nomeReg, $novo.Count)
                        foreach ($a in $anteriores) {
                            Remove-Item $a.FullName -Force
                            Escreve "registo de apagamentos substituido: removido $($a.Name)"
                        }
                    }
                }
            }
        }
    }
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
