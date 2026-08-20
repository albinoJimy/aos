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
    [switch]$Puxar
)

$ErrorActionPreference = 'Stop'
$tmp = $null

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

    if ($Puxar) {
        Passo "1. A PUXAR o backup mais recente do servidor"
        & (Join-Path $PSScriptRoot 'pull-backups.ps1') -Destino $Backups
    }

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

    Passo "6. O QUE FALTA, e nao e este script que o faz"
    Nota "Para o no PASSAR A VERIFICAR, as tres variaveis tem de ir para o servidor JUNTAS"
    Nota "(algumas ⇒ ErrWormAnchorIncomplete, aborta o arranque):"
    Nota "  AOS_WORM_TRUST_ANCHOR        = a PUBLICA do selador, em hex"
    Nota "  AOS_WORM_CHECKPOINT_FILE     = caminho montado do checkpoints.json"
    Nota "  AOS_WORM_EXPECTED_HEADS_FILE = caminho montado do heads.json"
    Nota ""
    Nota "E a cobertura NUNCA e total: as particoes nascem por run, logo o run seguinte cria uma"
    Nota "que esta ancora nao cobre. Ancorado ate ao ultimo selo; depois disso, so re-encadeamento."
}
finally {
    if ($tmp -and (Test-Path $tmp)) {
        # Dados de PRODUCAO decifrados. Vao-se embora sempre — incluindo quando a selagem falha,
        # que e precisamente quando alguem estaria distraido a ler o erro.
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }
}
