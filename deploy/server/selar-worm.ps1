<#
.SYNOPSIS
  Sela o WORM de producao a partir da copia do backup, e produz as duas metades da ancora.

.DESCRIPTION
  Fecha o ultimo passo que faltava a verificacao ancorada (README §8, ponto 8). O codigo estava
  pronto dos dois lados desde 2026-08-20 — `aos-issuer worm-seal` emite um checkpoint POR
  PARTICAO, e o no recebe-os em conjunto — e o que faltava era operacional: custodia da chave,
  selagem contra a copia off-host, e uma cadencia.

  PORQUE CONTRA O BACKUP E NAO CONTRA O SERVIDOR. `audit.Signer.Seal` precisa do STORE para ler
  o hash de entrada, e o store vive onde a chave NAO pode estar (molde AOS-156: a chave assina
  FORA do no). A copia que o backup ja traz off-host e a unica que satisfaz as duas condicoes.

  O QUE ESTE SCRIPT NAO PROVA, e convem que fique dito antes de alguem se convencer do contrario:
  a ancora prova que a cadeia NAO MUDOU DESDE A SELAGEM. Nao prova que era honesta ANTES dela. O
  que se fecha e a truncatura do tail e a reescrita desde a genese POSTERIORES ao selo.

  E O LIMITE QUE A CADENCIA NAO FECHA: as particoes nascem POR RUN, logo o run seguinte cria uma
  que nenhuma selagem anterior cobre. «Ancorado ate ao ultimo selo; depois disso, so
  re-encadeamento.» Selar mais vezes ENCOLHE a janela; nao a fecha.

.EXAMPLE
  # ciclo completo: puxa o backup mais recente, sela, e roda os ficheiros
  powershell -ExecutionPolicy Bypass -File deploy\server\selar-worm.ps1 -Puxar

.EXAMPLE
  # selar contra o backup que ja esta em disco (sem tocar no servidor)
  powershell -ExecutionPolicy Bypass -File deploy\server\selar-worm.ps1
#>
[CmdletBinding()]
param(
    [string]$Backups   = "$env:USERPROFILE\aos-backups",
    [string]$Chave     = "C:\Jimy\aos\deploy\server\secrets-local\wormseal.key",
    [string]$ChaveBkp  = "C:\Jimy\aos\deploy\server\secrets-local\backup-key\backup.key",
    [string]$Issuer    = "C:\Jimy\aos\packages\cmd\aos-issuer",
    # As duas metades vao para directorios SEPARADOS, e isso e do desenho: o piso de frescura
    # existe para recusar um checkpoint LEGITIMO mas ANTERIOR, reapresentado para mascarar a
    # truncatura do que veio depois. Se viajassem no mesmo ficheiro, quem trocasse o ficheiro
    # trocava os dois — e o piso deixaria de morder exactamente no ataque que existe para fechar.
    #
    # HONESTIDADE SOBRE ESTA SEPARACAO: numa so maquina, quem chega a um directorio chega ao
    # outro. A separacao so vale a serio quando os dois sao sincronizados para sitios com
    # controlos DIFERENTES. Aqui prepara-se a forma; a substancia depende de para onde vao.
    [string]$Ancoras   = "C:\Jimy\aos\deploy\server\secrets-local\ancoras",
    [string]$Pisos     = "C:\Jimy\aos\deploy\server\secrets-local\pisos",
    [switch]$Puxar,
    # -PorSSH: traz o `worm.wal` VIVO do servidor em vez de o extrair do backup cifrado.
    #
    # PORQUE EXISTE, e a razao e de EXPOSICAO e nao de comodidade. A selagem diaria corre sozinha;
    # pelo caminho do backup teria de alcancar DUAS chaves privadas sem ninguem presente — a do
    # selador (forja ancoras) e a `backup.key`, que decifra TODAS as copias de producao, incluindo
    # a base do IdP. Quem comprometesse esta maquina durante a janela diaria levava as duas. Por
    # SSH, a tarefa precisa da chave de DEPLOY e da do selador; a `backup.key` fica de fora.
    #
    # O QUE SE PERDE, e fica dito: o WORM viaja FORA do envelope do backup, protegido so pelo
    # transporte. E o que se ganha e maior do que isso, porque a `backup.key` abre tudo o resto.
    #
    # CONSISTENCIA DA COPIA VIVA: o no escreve no ficheiro enquanto se copia, logo apanha-se um
    # PREFIXO — e um prefixo de hash-chain e uma cadeia valida truncada. O `backup.sh` ja tem
    # exactamente a mesma propriedade e declara-a. NAO produz falsos alarmes de recuo: o WAL e
    # append-only, portanto o prefixo de hoje CONTEM o de ontem, e um `head` nunca desce por causa
    # de uma leitura rasgada. Se algum dia o WAL passar a ser compactado, isto deixa de valer — e
    # ai o alarme de recuo estaria certo a disparar.
    #
    # NAO SE MISTURAM AS DUAS FONTES PARA TRAS, e descobri-o a correr as duas seguidas: depois de
    # selar do WORM VIVO, selar de um backup ANTERIOR e um RECUO — e o `exigirContinuidade`
    # recusa, com a mesma mensagem que significaria «alguem truncou o teu trilho». Nao e defeito:
    # e a guarda a fazer o que existe para fazer. A regra de operacao que daqui sai e simples —
    # escolha uma fonte para a cadencia e so avance no tempo. O modo de backup fica para quando o
    # servidor estiver inalcancavel ou sob suspeita, e nesse caso a ancora seguinte comeca de novo.
    [switch]$PorSSH,
    [string]$Servidor  = "aos@37.60.241.150",
    [string]$ChaveSSH  = "C:\Jimy\aos\deploy\server\secrets-local\deploy_key",
    [string]$KnownHosts = "C:\Jimy\aos\deploy\server\secrets-local\known_hosts.txt"
)

$ErrorActionPreference = 'Stop'
$tmp = $null
# Declarado AQUI e nao dentro do bloco try: o finally le-o, e uma falha antes da atribuicao
# deixaria a limpeza a decidir sobre uma variavel que nunca existiu.
$remoto = $false


# Nativo — corre um executavel externo SEM que o stderr dele mate o script.
#
# MESMO PADRAO do `pull-backups.ps1` (procurei antes de escrever). Com
# $ErrorActionPreference='Stop', qualquer linha que um executavel escreva em stderr vira erro
# TERMINANTE em PowerShell 5.1 — mesmo quando o comando teve sucesso.
#
# CUSTOU-ME UM WORM DE PRODUCAO ESQUECIDO NUM SERVIDOR. O ssh emitiu um aviso sobre uma chave
# inacessivel, o script morreu DEPOIS de a extraccao ja ter corrido, e a limpeza — que vive no
# `finally` — morreu pela MESMA razao antes de apagar o que ficara la. A falha aconteceu num
# teste de falha deliberado, que e o unico sitio onde queria que acontecesse.
#
# O sucesso passa a medir-se por $LASTEXITCODE, que e o que sempre devia ter sido.
function Nativo([scriptblock]$bloco) {
    $anterior = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try { & $bloco } finally { $ErrorActionPreference = $anterior }
}
function Passo($t) { Write-Host "`n$t" -ForegroundColor Cyan }
function Bom($t)   { Write-Host "  $t" -ForegroundColor Green }
function Mau($t)   { Write-Host "  $t" -ForegroundColor Red }
function Nota($t)  { Write-Host "  $t" -ForegroundColor DarkGray }

try {
    foreach ($d in @($Ancoras, $Pisos)) {
        if (-not (Test-Path $d)) { New-Item -ItemType Directory -Path $d -Force | Out-Null }
    }
    if (-not (Test-Path $Chave)) {
        Mau "A chave do selador NAO existe em $Chave"
        Nota "Gere-a (o valor nunca passa por lado nenhum senao por si):"
        Nota ("  openssl rand -hex 32 > " + $Chave)
        throw "chave do selador ausente"
    }

    if ($Puxar -and -not $PorSSH) {
        Passo "1. A PUXAR o backup mais recente do servidor"
        & (Join-Path $PSScriptRoot 'pull-backups.ps1') -Destino $Backups
    }

    if ($PorSSH) {
        Passo "2. A TRAZER o worm.wal VIVO do servidor (sem tocar na backup.key)"
        $sshArgs = @('-i', $ChaveSSH, '-o', 'IdentitiesOnly=yes', '-o', 'BatchMode=yes',
                     '-o', "UserKnownHostsFile=$KnownHosts")
        $tmp = Join-Path ([IO.Path]::GetTempPath()) ("aos-selo-" + [Guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $tmp -Force | Out-Null
        $remoto = $true

        # O worm.wal e 600 e pertence ao uid 65532 (distroless): o utilizador de deploy NAO o le.
        # A copia corre como root DENTRO do contentor e entrega logo a posse, para o ficheiro nunca
        # ficar legivel a terceiros — este servidor NAO e dedicado (README §1).
        # BASE64, e nao e adorno. Entre aqui e o `cp` ha QUATRO camadas de citacao — PowerShell,
        # ssh, o shell remoto, o docker e o `sh -c` de dentro do contentor. O PowerShell 5.1 nao
        # escapa aspas ao passar argumentos a um executavel nativo, e o comando chegava truncado:
        # o `cp` do BusyBox respondia com a sua pagina de ajuda. Codificar o script inteiro reduz
        # as quatro camadas a uma, e o que viaja passa a ser um blob sem aspas nenhumas.
        $script = 'set -e' + "`n" +
                  'mkdir -p ~/selo && chmod 700 ~/selo' + "`n" +
                  'U=$(id -u); G=$(id -g)' + "`n" +
                  'docker run --rm -v aos_aos-data:/aos:ro -v "$HOME/selo":/out alpine:3.20 \' + "`n" +
                  '  sh -c "cp /aos/worm.wal /out/worm.wal && chown $U:$G /out/worm.wal && chmod 600 /out/worm.wal"' + "`n"
        $b64 = [Convert]::ToBase64String([Text.Encoding]::ASCII.GetBytes($script))
        $extrair = 'echo ' + $b64 + ' | base64 -d | sh'
        Nativo { & ssh @sshArgs $Servidor $extrair }
        $codigo = $LASTEXITCODE
        # 255 e o codigo do ssh para «nem houve ligacao». Distinguir isso de uma falha DEPOIS de
        # ligar e o que impede a limpeza de gritar em cada falha de rede — um aviso que dispara
        # sempre deixa de ser aviso, e este e o que diz que ficou um WORM de producao no servidor.
        if ($codigo -eq 255) { $remoto = $false }
        if ($codigo -ne 0) { throw "extraccao remota do worm.wal falhou ($codigo)" }

        Nativo { & scp -q @sshArgs "${Servidor}:selo/worm.wal" (Join-Path $tmp 'worm.wal') }
        if ($LASTEXITCODE -ne 0) { throw "scp do worm.wal falhou ($LASTEXITCODE)" }
        $worm = Join-Path $tmp 'worm.wal'
        Bom ("worm.wal VIVO: {0:N0} bytes" -f (Get-Item $worm).Length)
        Nota "(o backup nao foi tocado, e a backup.key nao entrou nesta execucao)"
    } else {
    Passo "2. A ESCOLHER a copia mais recente"
    $enc = Get-ChildItem -Path $Backups -Filter '*.tar.gz.enc' -ErrorAction SilentlyContinue |
           Sort-Object LastWriteTime -Descending | Select-Object -First 1
    if (-not $enc) { throw "nenhum backup em $Backups (corra com -Puxar)" }
    Bom ("{0}  ({1:N0} bytes, {2:yyyy-MM-dd HH:mm}Z)" -f $enc.Name, $enc.Length, $enc.LastWriteTimeUtc)

    Passo "3. A DECIFRAR e a extrair o WORM"
    # Tudo o que sai daqui e dado de PRODUCAO — incluindo a base do IdP. Vive num temporario que
    # o `finally` apaga, e nao no directorio de trabalho.
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ("aos-selo-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp -Force | Out-Null
    & openssl smime -decrypt -binary -inform DER -in $enc.FullName -inkey $ChaveBkp -out (Join-Path $tmp 'bundle.tar.gz')
    if ($LASTEXITCODE -ne 0) { throw "openssl smime falhou ($LASTEXITCODE)" }
    Push-Location $tmp
    try {
        & tar -xzf 'bundle.tar.gz'
        if ($LASTEXITCODE -ne 0) { throw "tar do bundle falhou" }
        & tar -xzf 'volumes.tar.gz' 'aos/worm.wal'
        if ($LASTEXITCODE -ne 0) { throw "tar dos volumes falhou (aos/worm.wal ausente?)" }
    } finally { Pop-Location }
    $worm = Join-Path $tmp 'aos\worm.wal'
    if (-not (Test-Path $worm)) { throw "worm.wal nao apareceu na extraccao" }
    Bom ("worm.wal: {0:N0} bytes" -f (Get-Item $worm).Length)
    }

    Passo "4. A SELAR"
    $anteriorFile = Join-Path $Ancoras 'checkpoints.json'
    $temAnterior  = Test-Path $anteriorFile
    if ($temAnterior) {
        Nota "selagem anterior encontrada — a continuidade vai ser EXIGIDA"
        Nota "(mesma VerifyFromCheckpoint que o no corre no arranque; divergencia RECUSA selar)"
    } else {
        Nota "PRIMEIRA selagem: nao ha contra o que comparar, e a guarda de continuidade e"
        Nota "comparativa, nao absoluta. Esta ancora vale a partir de agora, nao para tras."
    }

    $argsBase = @('run', '.', 'worm-seal', '--worm', $worm, '--key-file', $Chave)
    if ($temAnterior) { $argsBase += @('--anterior', $anteriorFile) }

    Push-Location $Issuer
    try {
        $cps = & go @argsBase 2>&1
        if ($LASTEXITCODE -ne 0) { Mau ($cps -join "`n"); throw "worm-seal (checkpoints) falhou" }
        $argsHeads = $argsBase + '--heads'
        $heads = & go @argsHeads 2>&1
        if ($LASTEXITCODE -ne 0) { Mau ($heads -join "`n"); throw "worm-seal (heads) falhou" }
    } finally { Pop-Location }

    Passo "5. A RODAR os ficheiros"
    $stamp = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
    if ($temAnterior) {
        # A anterior fica GUARDADA, e nao substituida. Se uma selagem futura recusar por
        # divergencia, e este ficheiro que diz contra o que ela recusou.
        Copy-Item $anteriorFile (Join-Path $Ancoras "checkpoints-$stamp.json") -Force
    }
    # SEM BOM, e nao e detalhe de estilo: o `Set-Content -Encoding utf8` do PowerShell 5.1 escreve
    # BOM, e o `encoding/json` do Go recusa-o com «invalid character 'i'». Descobri-o a correr
    # este script DUAS vezes: a segunda nao conseguiu ler o ficheiro que a primeira escreveu.
    #
    # O no le estes MESMOS ficheiros. Com BOM, o arranque abortaria em ErrBadWormCheckpoint — a
    # postura certa (fail-closed), com um diagnostico que ninguem liga a codificacao.
    $semBom = New-Object Text.UTF8Encoding $false
    [IO.File]::WriteAllText($anteriorFile, ($cps -join "`n"), $semBom)
    [IO.File]::WriteAllText((Join-Path $Pisos 'heads.json'), ($heads -join "`n"), $semBom)

    # `@(...)` a volta de um ConvertFrom-Json NAO conta os elementos em PS 5.1: o array chega ao
    # pipeline como UM objecto, e a contagem dava 1. Dizia «1 particao ancorada» sobre um ficheiro
    # com 120 — exactamente a falha «1 em 108» que o README descrevia, agora so na mensagem.
    $n = (ConvertFrom-Json ($cps -join "`n")).Count
    Bom ("{0} particao(oes) ancorada(s)" -f $n)
    Bom ("checkpoints -> " + $anteriorFile)
    Bom ("pisos       -> " + (Join-Path $Pisos 'heads.json'))

    Passo "6. A ANCORA, e o que falta para o no a USAR"
    # A PUBLICA sai daqui e nao do operador a descobri-la. A primeira versao deste script pedia
    # «a PUBLICA do selador, em hex» e nao dizia onde a ir buscar — deixava por fazer o unico
    # passo que o script podia fazer sozinho, e que nao envolve segredo nenhum: a pubkey e para
    # ser PUBLICADA, e vai literalmente para uma variavel de ambiente no servidor.
    Push-Location $Issuer
    try { $pub = (& go run . pubkey --key-file $Chave 2>&1 | Select-Object -Last 1).ToString().Trim() }
    finally { Pop-Location }
    if ($pub -notmatch '^[0-9a-f]{64}$') { throw "pubkey do selador invalida: $pub" }

    Nota "As tres em conjunto ou NENHUMA (algumas -> ErrWormAnchorIncomplete, aborta o arranque):"
    Write-Host ("    AOS_WORM_TRUST_ANCHOR={0}" -f $pub) -ForegroundColor Green
    Nota "    AOS_WORM_CHECKPOINT_FILE     = caminho MONTADO do checkpoints.json"
    Nota "    AOS_WORM_EXPECTED_HEADS_FILE = caminho MONTADO do heads.json"
    Nota ""
    Nota "E a cobertura NUNCA e total: as particoes nascem por run, logo o run seguinte cria uma"
    Nota "que esta ancora nao cobre. Ancorado ate ao ultimo selo; depois disso, so re-encadeamento."
}
finally {
    if ($remoto) {
        # A copia do lado do SERVIDOR vai-se embora SEMPRE — incluindo quando a selagem falha, que
        # e precisamente quando alguem estaria distraido a ler o erro.
        #
        # `Nativo` + try/catch: NADA pode impedir esta limpeza de correr. Sem isto, o aviso do ssh
        # em stderr matava o proprio `finally` e o worm.wal ficava num home do servidor — foi o que
        # aconteceu no primeiro teste de falha.
        #
        # E VERIFICA-SE. Uma limpeza que falha em silencio e pior do que nao ter limpeza: deixa o
        # WORM de producao fora do volume, com toda a gente convencida de que nao ficou.
        try {
            $r = Nativo { & ssh -i $ChaveSSH -o IdentitiesOnly=yes -o BatchMode=yes `
                    -o "UserKnownHostsFile=$KnownHosts" $Servidor `
                    'rm -f ~/selo/worm.wal; rmdir ~/selo 2>/dev/null; ls -d ~/selo 2>/dev/null || echo LIMPO' 2>&1 }
            if (($r -join ' ') -match 'LIMPO') {
                Nota "servidor limpo (worm.wal removido)"
            } else {
                Mau "NAO consegui confirmar a limpeza do ~/selo no servidor — o worm.wal pode ter"
                Mau "ficado la. Corra: ssh $Servidor 'rm -rf ~/selo'"
            }
        } catch {
            Mau "a limpeza do servidor FALHOU ($_) — corra: ssh $Servidor 'rm -rf ~/selo'"
        }
    }
    if ($tmp -and (Test-Path $tmp)) {
        # Dados de PRODUCAO decifrados. Vao-se embora sempre — incluindo quando a selagem falha,
        # que e precisamente quando alguem estaria distraido a ler o erro.
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }
}
