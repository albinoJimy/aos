<#
.SYNOPSIS
  Sela o WORM de producao e entrega as duas metades da ancora ao servidor, com uma chave que so faz isso.

.DESCRIPTION
  Fecha o ultimo passo que faltava a verificacao ancorada (README §8, ponto 8). O codigo estava
  pronto dos dois lados desde 2026-08-20 — `aos-issuer worm-seal` emite um checkpoint POR
  PARTICAO, e o no recebe-os em conjunto — e o que faltava era operacional: custodia da chave,
  selagem off-host, e uma cadencia.

  PORQUE A CHAVE PRIVADA NUNCA VAI AO SERVIDOR. `audit.Signer.Seal` precisa do STORE para ler
  o hash de entrada, e o store vive onde a chave NAO pode estar (molde AOS-156: a chave assina
  FORA do no). Por isso o WORM vem ate aqui — pelo gate (-PorSSH) ou dentro do backup.

  O QUE ESTE SCRIPT NAO PROVA, e convem que fique dito antes de alguem se convencer do contrario:
  a ancora prova que a cadeia NAO MUDOU DESDE A SELAGEM. Nao prova que era honesta ANTES dela. O
  que se fecha e a truncatura do tail e a reescrita desde a genese POSTERIORES ao selo.

  E O LIMITE QUE A CADENCIA NAO FECHA: as particoes nascem POR RUN, logo o run seguinte cria uma
  que nenhuma selagem anterior cobre. «Ancorado ate ao ultimo selo; depois disso, so
  re-encadeamento.» Selar mais vezes ENCOLHE a janela; nao a fecha.

  A CHAVE SSH SO SELA. A tarefa diaria corre sozinha, pelo que a chave nao tem passphrase — e o
  `aos` esta no grupo docker, onde uma shell e root no servidor. A versao anterior usava a
  `deploy_key` (shell); perdeu-se, e refaze-la assim poria o host numa maquina de secretaria. A
  chave e DEDICADA (secrets-local/worm-seal/) e o servidor forca-lhe um comando,
  `worm-seal-gate.sh`, que so aceita: `worm`, `scp -t` para os dois nomes temporarios, e
  `trocar`. Ver deploy/server/README.md §8, «A tarefa diaria».

  CONTINUIDADE OBRIGATORIA, SEM EXCEPCAO. Todas as selagens correm com `--anterior`: sem
  checkpoints em vigor este script RECUSA. Uma selagem sem anterior nao compara nada, e numa
  tarefa que corre sozinha seria a porta por onde uma truncatura passava a ser ancorada.
  ESTE SCRIPT E A CADENCIA, NAO A ROTACAO: a primeira selagem depois de rodar a chave do selador
  faz-se A MAO, de proposito — passos (b) a (d) da seccao «Rotacao das chaves de autoridade» do
  README — e o par novo fica em secrets-local (passo (g)) para esta tarefa o poder usar no
  `--anterior` do dia seguinte.

  O NO SO LE A ANCORA NO ARRANQUE. Entregar nao obriga a reiniciar nada, e nada muda no no ate ao
  proximo restart — incluindo a metrica `aos_worm_anchor_age_seconds`, que conta desde a selagem
  que o no CARREGOU (ver README §8).

.EXAMPLE
  # o ciclo diario: WORM vivo pelo gate, sela com continuidade, entrega pelo gate
  powershell -ExecutionPolicy Bypass -File deploy\server\selar-worm.ps1 -PorSSH -Entregar

.EXAMPLE
  # servidor inalcancavel ou sob suspeita: selar contra o backup que ja esta em disco
  powershell -ExecutionPolicy Bypass -File deploy\server\selar-worm.ps1
#>
[CmdletBinding()]
param(
    [string]$Backups   = "$env:USERPROFILE\aos-backups",
    [string]$Chave     = "C:\Jimy\AOS\deploy\server\secrets-local\wormseal.key",
    [string]$ChaveBkp  = "C:\Jimy\AOS\deploy\server\secrets-local\backup-key\backup.key",
    [string]$Issuer    = "C:\Jimy\AOS\packages\cmd\aos-issuer",
    # Um `aos-issuer.exe` ja compilado, em vez de `go run` em -Issuer. Vazio = `go run`.
    [string]$IssuerExe = "",
    # As duas metades vao para directorios SEPARADOS, e isso e do desenho: o piso de frescura
    # existe para recusar um checkpoint LEGITIMO mas ANTERIOR, reapresentado para mascarar a
    # truncatura do que veio depois. Se viajassem no mesmo ficheiro, quem trocasse o ficheiro
    # trocava os dois — e o piso deixaria de morder exactamente no ataque que existe para fechar.
    #
    # HONESTIDADE SOBRE ESTA SEPARACAO: numa so maquina, quem chega a um directorio chega ao
    # outro. A separacao so vale a serio quando os dois sao sincronizados para sitios com
    # controlos DIFERENTES. Aqui prepara-se a forma; a substancia depende de para onde vao.
    [string]$Ancoras   = "C:\Jimy\AOS\deploy\server\secrets-local\ancoras",
    [string]$Pisos     = "C:\Jimy\AOS\deploy\server\secrets-local\pisos",
    [switch]$Puxar,
    # -PorSSH: traz o `worm.wal` VIVO do servidor em vez de o extrair do backup cifrado.
    #
    # PORQUE EXISTE, e a razao e de EXPOSICAO e nao de comodidade. A selagem diaria corre sozinha;
    # pelo caminho do backup teria de alcancar DUAS chaves privadas sem ninguem presente — a do
    # selador (forja ancoras) e a `backup.key`, que decifra TODAS as copias de producao, incluindo
    # a base do IdP. Por SSH, a tarefa precisa da chave do gate e da do selador; a `backup.key`
    # fica de fora.
    #
    # O QUE SE PERDE, e fica dito: o WORM viaja FORA do envelope do backup, protegido so pelo
    # transporte. E o que se ganha e maior do que isso, porque a `backup.key` abre tudo o resto.
    #
    # CONSISTENCIA DA COPIA VIVA: o no escreve no ficheiro enquanto se le, logo apanha-se um
    # PREFIXO — e um prefixo de hash-chain e uma cadeia valida truncada (o OpenFileStore descarta
    # um registo rasgado no fim). NAO produz falsos alarmes de recuo: o WAL e append-only, portanto
    # o prefixo de hoje CONTEM o de ontem. Se algum dia o WAL passar a ser compactado, isto deixa
    # de valer — e ai o alarme de recuo estaria certo a disparar.
    #
    # NAO SE MISTURAM AS DUAS FONTES PARA TRAS: depois de selar do WORM VIVO, selar de um backup
    # ANTERIOR e um RECUO — e o `exigirContinuidade` recusa, com a mesma mensagem que significaria
    # «alguem truncou o teu trilho». Escolha-se uma fonte para a cadencia e so se avance no tempo.
    [switch]$PorSSH,
    # -Entregar: leva os dois ficheiros ao servidor depois de selar. Ver o passo 7.
    [switch]$Entregar,
    [string]$Servidor  = "aos@37.60.241.150",
    [int]$Porta        = 22,
    [string]$ChaveSSH  = "C:\Jimy\AOS\deploy\server\secrets-local\worm-seal\id_ed25519",
    # Vazio = o known_hosts do utilizador. So existe para o ensaio contra um sshd descartavel.
    # NAO se desliga a verificacao do host.
    [string]$KnownHosts = ""
)

$ErrorActionPreference = 'Stop'
$tmp = $null

# Nativo — corre um executavel externo SEM que o stderr dele mate o script.
#
# MESMO PADRAO do `pull-backups.ps1`. Com $ErrorActionPreference='Stop', qualquer linha que um
# executavel escreva em stderr vira erro TERMINANTE em PowerShell 5.1 — mesmo quando o comando
# teve sucesso. O sucesso mede-se por $LASTEXITCODE.
function Nativo([scriptblock]$bloco) {
    $anterior = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try { & $bloco } finally { $ErrorActionPreference = $anterior }
}

# ParaFicheiro — corre um executavel e escreve o STDOUT dele, byte a byte, num ficheiro.
#
# NAO `& exe > ficheiro`, nem capturar para uma variavel: o PowerShell 5.1 decide que o stdout de
# um executavel nativo e TEXTO — descodifica-o na pagina de codigo da consola e re-escreve-o em
# UTF-16. O `worm.wal` sairia corrompido; e o JSON do selador, capturado com `2>&1` como estava,
# levava para dentro do ficheiro qualquer aviso que o `go` escrevesse em stderr. Aqui os dois
# canais ficam separados: o stdout vai inteiro para o ficheiro, o stderr volta para o diagnostico.
function ParaFicheiro([string]$exe, [string[]]$argumentos, [string]$destino, [string]$dir = "") {
    $psi = New-Object Diagnostics.ProcessStartInfo
    $psi.FileName = $exe
    $psi.Arguments = ($argumentos | ForEach-Object {
        if ($_ -eq '' -or $_ -match '[\s"]') { '"' + ($_ -replace '(\\*)"', '$1$1\"' -replace '(\\+)$', '$1$1') + '"' } else { $_ }
    }) -join ' '
    $psi.UseShellExecute = $false
    $psi.RedirectStandardInput = $true   # e fecha-se ja: numa tarefa agendada nao ha stdin (= ssh -n)
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    if ($dir) { $psi.WorkingDirectory = $dir }
    $p = [Diagnostics.Process]::Start($psi)
    $p.StandardInput.Close()
    # O stderr le-se em paralelo: se o processo encher o buffer do stderr enquanto se copia o
    # stdout, os dois ficam a espera um do outro para sempre.
    $erro = $p.StandardError.ReadToEndAsync()
    $fs = [IO.File]::Open($destino, [IO.FileMode]::Create, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try { $p.StandardOutput.BaseStream.CopyTo($fs) } finally { $fs.Close() }
    $p.WaitForExit()
    [pscustomobject]@{ Codigo = $p.ExitCode; Erro = $erro.Result.Trim() }
}

function Passo($t) { Write-Host "`n$t" -ForegroundColor Cyan }
function Bom($t)   { Write-Host "  $t" -ForegroundColor Green }
function Mau($t)   { Write-Host "  $t" -ForegroundColor Red }
function Nota($t)  { Write-Host "  $t" -ForegroundColor DarkGray }

function Executavel($nome) {
    $c = Get-Command $nome -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $c) { throw "$nome nao esta no PATH" }
    $c.Source
}

# O aos-issuer: o binario, se foi dado; senao `go run` no directorio do modulo.
function Issuer([string[]]$sub, [string]$destino) {
    if ($IssuerExe) { return ParaFicheiro $IssuerExe $sub $destino }
    ParaFicheiro (Executavel 'go') (@('run', '.') + $sub) $destino $Issuer
}

function Sha256($f) { (Get-FileHash -Algorithm SHA256 -LiteralPath $f).Hash.ToLowerInvariant() }
$semBom = New-Object Text.UTF8Encoding $false

# BatchMode: a tarefa nunca fica parada a pedir uma password. StrictHostKeyChecking fica no default
# (ask, que em BatchMode e recusa): um servidor com outra chave de host nao recebe pedidos.
# ServerAlive: o ConnectTimeout so cobre o ESTABELECER da ligacao; uma sessao que pendura depois de
# aberta ficava ate ao ExecutionTimeLimit da tarefa (visto na AOS-RecolherBackups, 2026-09-14).
$sshOpts = @('-i', $ChaveSSH, '-o', 'IdentitiesOnly=yes', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=20',
             '-o', 'ServerAliveInterval=15', '-o', 'ServerAliveCountMax=4')
if ($KnownHosts) { $sshOpts += @('-o', "UserKnownHostsFile=$KnownHosts") }

try {
    foreach ($d in @($Ancoras, $Pisos)) {
        if (-not (Test-Path $d)) { New-Item -ItemType Directory -Path $d -Force | Out-Null }
    }
    if (-not (Test-Path $Chave)) {
        Mau "A chave do selador NAO existe em $Chave"
        throw "chave do selador ausente"
    }
    if ($PorSSH -or $Entregar) {
        # Antes de ligar, e nao depois: sem isto, uma chave em falta dava um aviso do ssh em stderr
        # e um codigo 255 que se le como «servidor inalcancavel».
        if (-not (Test-Path $ChaveSSH)) {
            Mau "A chave SSH da selagem NAO existe em $ChaveSSH (ver README §8, «A tarefa diaria»)"
            Nota "Passe outra com -ChaveSSH <caminho>; tem de estar autorizada com o comando forcado do gate."
            throw "chave SSH ausente"
        }
        if ($KnownHosts -and -not (Test-Path $KnownHosts)) {
            Mau "O known_hosts NAO existe em $KnownHosts"
            Nota "Passe outro com -KnownHosts <caminho>, ou deixe vazio para usar o do utilizador."
            Nota "Nao se desliga a verificacao do host."
            throw "known_hosts ausente"
        }
    }

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ("aos-selo-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp -Force | Out-Null

    # A PUBLICA do selador, derivada ANTES de selar: e o que permite reconhecer que a chave MUDOU
    # desde a selagem anterior, e e o que o passo 6 imprime.
    $pubFile = Join-Path $tmp 'pub.txt'
    $r = Issuer @('pubkey', '--key-file', $Chave) $pubFile
    $pub = (Get-Content $pubFile | Where-Object { $_ -match '^[0-9a-f]{64}$' } | Select-Object -Last 1)
    if ($r.Codigo -ne 0 -or -not $pub) { Mau $r.Erro; throw "pubkey do selador invalida" }

    # A CONTINUIDADE verifica-se ANTES de tocar no servidor: sem ela nao ha selagem, e nao vale a
    # pena trazer o WORM de producao para depois recusar.
    $anteriorFile = Join-Path $Ancoras 'checkpoints.json'
    $pisosFile    = Join-Path $Pisos 'heads.json'
    # selador.pub: a publica que assinou a selagem guardada em $Ancoras. O checkpoint nao traz
    # identidade da chave, pelo que sem este ficheiro uma chave trocada so se revelava como «o WORM
    # DIVERGIU» — lido como adulteracao, quando e so a chave. Ficheiro ausente (selagens anteriores
    # a esta guarda) => nao ha contra o que comparar, e segue-se em frente.
    $seladorFile  = Join-Path $Ancoras 'selador.pub'

    if (-not (Test-Path $anteriorFile)) {
        Mau "Nao ha checkpoints em vigor em $anteriorFile"
        Mau "A continuidade e OBRIGATORIA: sem --anterior nao se sela."
        Nota "Se ACABOU de rodar a chave do selador, a primeira selagem faz-se A MAO — passos (b) a (d)"
        Nota "da seccao «Rotacao das chaves de autoridade» do README — e o par novo fica aqui no passo (g)."
        throw "sem checkpoints em vigor — selagem sem continuidade recusada"
    }
    if (Test-Path $seladorFile) {
        $pubAnterior = (Get-Content $seladorFile -Raw).Trim()
        if ($pubAnterior -ne $pub) {
            Mau "A chave do selador MUDOU desde a selagem anterior:"
            Mau "  anterior: $pubAnterior"
            Mau "  agora:    $pub"
            Nota "Se a rodou de proposito: a rotacao faz-se A MAO (README, «Rotacao das chaves de"
            Nota "autoridade»), e so depois do passo (g) — com o par novo em $Ancoras — e que esta"
            Nota "tarefa volta a correr. Mantenha-a suspensa ate la."
            Nota "Se NAO a rodou, a chave em $Chave nao e a que devia ser: pare aqui."
            throw "chave do selador diferente da selagem anterior"
        }
    }

    if ($Puxar -and -not $PorSSH) {
        Passo "1. A PUXAR o backup mais recente do servidor"
        & (Join-Path $PSScriptRoot 'pull-backups.ps1') -Destino $Backups
    }

    if ($PorSSH) {
        Passo "2. A TRAZER o worm.wal VIVO pelo gate (sem tocar na backup.key)"
        # Um so pedido, `worm`, e o gate faz o resto: le o volume como o uid do no, sem rede e sem
        # escrever nada. NAO ha copia no servidor — a versao anterior deixava `~/selo/worm.wal` num
        # home e precisava de uma limpeza verificada para o apagar.
        $worm = Join-Path $tmp 'worm.wal'
        $r = ParaFicheiro (Executavel 'ssh') (@('-n') + $sshOpts + @('-p', "$Porta", $Servidor, 'worm')) $worm
        if ($r.Codigo -ne 0) {
            if ($r.Erro) { Mau $r.Erro }
            throw "leitura do worm.wal pelo gate falhou ($($r.Codigo))"
        }
        $bytes = (Get-Item $worm).Length
        if ($bytes -eq 0) { throw "o gate devolveu um worm.wal VAZIO — nao se sela sobre nada" }
        Bom ("worm.wal VIVO: {0:N0} bytes" -f $bytes)
        Nota "(o backup nao foi tocado, e a backup.key nao entrou nesta execucao)"
    } else {
        Passo "2. A ESCOLHER a copia mais recente"
        $enc = Get-ChildItem -Path $Backups -Filter '*.tar.gz.enc' -ErrorAction SilentlyContinue |
               Sort-Object LastWriteTime -Descending | Select-Object -First 1
        if (-not $enc) { throw "nenhum backup em $Backups (corra com -Puxar)" }
        Bom ("{0}  ({1:N0} bytes, {2:yyyy-MM-dd HH:mm}Z)" -f $enc.Name, $enc.Length, $enc.LastWriteTimeUtc)

        Passo "3. A DECIFRAR e a extrair o WORM"
        # Tudo o que sai daqui e dado de PRODUCAO — incluindo a base do IdP. Vive no temporario que
        # o `finally` apaga.
        Nativo { & openssl smime -decrypt -binary -inform DER -in $enc.FullName -inkey $ChaveBkp -out (Join-Path $tmp 'bundle.tar.gz') }
        if ($LASTEXITCODE -ne 0) { throw "openssl smime falhou ($LASTEXITCODE)" }
        Push-Location $tmp
        try {
            Nativo { & tar -xzf 'bundle.tar.gz' }
            if ($LASTEXITCODE -ne 0) { throw "tar do bundle falhou" }
            Nativo { & tar -xzf 'volumes.tar.gz' 'aos/worm.wal' }
            if ($LASTEXITCODE -ne 0) { throw "tar dos volumes falhou (aos/worm.wal ausente?)" }
        } finally { Pop-Location }
        $worm = Join-Path $tmp 'aos\worm.wal'
        if (-not (Test-Path $worm)) { throw "worm.wal nao apareceu na extraccao" }
        Bom ("worm.wal: {0:N0} bytes" -f (Get-Item $worm).Length)
    }

    Passo "4. A SELAR, com continuidade face a ancora em vigor"
    Nota "(mesma VerifyFromCheckpoint que o no corre no arranque; divergencia ou recuo RECUSAM selar)"
    $cpNovo = Join-Path $tmp 'checkpoints.json'
    $hdNovo = Join-Path $tmp 'heads.json'
    $base = @('worm-seal', '--worm', $worm, '--key-file', $Chave, '--anterior', $anteriorFile)

    $r = Issuer $base $cpNovo
    if ($r.Codigo -ne 0) {
        Mau $r.Erro
        if ($r.Erro -match 'DIVERGIU') {
            Nota "Se RODOU a chave do selador, esta recusa e esperada: o --anterior e verificado contra"
            Nota "a pubkey que sela AGORA. A primeira selagem da rotacao faz-se A MAO (README, «Rotacao"
            Nota "das chaves de autoridade»), e esta tarefa so volta a correr depois do passo (g)."
        }
        throw "worm-seal (checkpoints) falhou"
    }
    $r = Issuer ($base + '--heads') $hdNovo
    if ($r.Codigo -ne 0) { Mau $r.Erro; throw "worm-seal (heads) falhou" }

    # SEM BOM, e nao e detalhe de estilo: o no e o `--anterior` da selagem seguinte leem estes
    # mesmos ficheiros. O selador escreve-os ele proprio (ParaFicheiro), pelo que isto e uma
    # asserção e nao uma conversao — e o gate recusa um BOM na entrega.
    foreach ($f in @($cpNovo, $hdNovo)) {
        $b = [IO.File]::ReadAllBytes($f)
        if ($b.Length -eq 0) { throw "o selador escreveu um ficheiro VAZIO: $f" }
        if ($b.Length -ge 3 -and $b[0] -eq 0xEF -and $b[1] -eq 0xBB -and $b[2] -eq 0xBF) { throw "BOM em $f" }
    }

    Passo "5. A RODAR os ficheiros locais"
    $stamp = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
    # A anterior fica GUARDADA, e nao substituida. Se uma selagem futura recusar por divergencia, e
    # este ficheiro que diz contra o que ela recusou. Os pisos guardam-se tambem: repor um par a
    # mao exige as DUAS metades da mesma selagem.
    Copy-Item $anteriorFile (Join-Path $Ancoras "checkpoints-$stamp.json") -Force
    if (Test-Path $pisosFile) { Copy-Item $pisosFile (Join-Path $Pisos "heads-$stamp.json") -Force }
    if (Test-Path $seladorFile) { Copy-Item $seladorFile (Join-Path $Ancoras "selador-$stamp.pub") -Force }
    Move-Item -Force $cpNovo $anteriorFile
    Move-Item -Force $hdNovo $pisosFile
    [IO.File]::WriteAllText($seladorFile, $pub, $semBom)

    # `@(...)` a volta de um ConvertFrom-Json NAO conta os elementos em PS 5.1: o array chega ao
    # pipeline como UM objecto, e a contagem dava 1.
    $n = (ConvertFrom-Json ([IO.File]::ReadAllText($anteriorFile))).Count
    Bom ("{0} particao(oes) ancorada(s)" -f $n)
    Bom ("checkpoints -> " + $anteriorFile)
    Bom ("pisos       -> " + $pisosFile)

    Passo "6. A ancora"
    Nota ("AOS_WORM_TRUST_ANCHOR={0}" -f $pub)
    Nota "E a cobertura NUNCA e total: as particoes nascem por run, logo o run seguinte cria uma"
    Nota "que esta ancora nao cobre. Ancorado ate ao ultimo selo; depois disso, so re-encadeamento."

    if ($Entregar) {
        Passo "7. A ENTREGAR a ancora ao servidor (pelo gate)"
        # ATOMICIDADE, e nao ordenacao. NENHUMA ordem entre os dois ficheiros e segura:
        #
        #   checkpoints primeiro -> as particoes novas ficam COM checkpoint e SEM piso, e o no
        #                           recusa arrancar (ErrBadWormExpectedHead);
        #   pisos primeiro       -> os checkpoints antigos ficam ABAIXO dos pisos novos, e o no
        #                           recusa arrancar (ErrCheckpointStale).
        #
        # Logo os dois sobem com nomes temporarios e o `trocar` do gate valida o PAR (esquema, que
        # e da mesma selagem, e que nenhuma particao recua face ao que la esta) e renomeia-os LADO A
        # LADO. A janela residual e o intervalo entre dois `mv`, e fica declarada: um arranque do no
        # exactamente ai apanharia um par incoerente e nao arrancaria. Recupera-se trocando outra vez.
        #
        # -O: protocolo classico. O gate so aceita `scp -t <um dos dois nomes temporarios>`; por
        # SFTP (o default do OpenSSH 9) o pedido e recusado, de proposito.
        foreach ($par in @(@($anteriorFile, '/opt/aos/ancoras/.checkpoints.novo'), @($pisosFile, '/opt/aos/pisos/.heads.novo'))) {
            $saida = Nativo { & scp -O -q @sshOpts -P "$Porta" $par[0] "${Servidor}:$($par[1])" 2>&1 | ForEach-Object { "$_" } }
            if ($LASTEXITCODE -ne 0) { $saida | ForEach-Object { Mau $_ }; throw "scp de $(Split-Path $par[0] -Leaf) falhou ($LASTEXITCODE)" }
        }

        $saida = Nativo { & ssh -n @sshOpts -p "$Porta" $Servidor 'trocar' 2>&1 | ForEach-Object { "$_" } }
        $codigo = $LASTEXITCODE
        $linha = $saida | Where-Object { $_ -match '^TROCADO [0-9a-f]{64} [0-9a-f]{64}$' } | Select-Object -Last 1
        if ($codigo -ne 0 -or -not $linha) {
            $saida | ForEach-Object { Mau $_ }
            throw "a troca no servidor foi RECUSADA ou falhou ($codigo) — o par em vigor no servidor nao mudou, excepto se a falha foi entre os dois mv: nesse caso corra isto outra vez"
        }
        # O servidor diz o que INSTALOU, e compara-se com o que se selou. Um `TROCADO` sem esta
        # comparacao seria o servidor a dizer que correu bem.
        $campos = $linha.Split(' ')
        if ($campos[1] -ne (Sha256 $anteriorFile) -or $campos[2] -ne (Sha256 $pisosFile)) {
            throw "o servidor instalou um par DIFERENTE do que foi selado aqui (sha256 nao bate)"
        }
        Bom "ancora entregue: checkpoints e pisos validados e trocados lado a lado (sha256 conferido)"
        Nota "o no so a LE no arranque: nao e preciso reinicia-lo, e nada muda nele ate ao proximo restart"
    }
}
finally {
    if ($tmp -and (Test-Path $tmp)) {
        # O WORM de producao (e, no modo backup, os dados decifrados). Vao-se embora sempre —
        # incluindo quando a selagem falha, que e precisamente quando alguem estaria distraido a ler
        # o erro.
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }
}
