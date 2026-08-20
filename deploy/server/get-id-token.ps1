<#
.SYNOPSIS
  Obtem um ID-token OIDC para um leitor HUMANO do AOS, por codigo de autorizacao + PKCE.

.DESCRIPTION
  O caminho anterior era o `password grant` (ROPC): a password do humano era escrita numa linha
  de comandos e entregue ao cliente. Isso tem tres defeitos que nao sao de estilo:

    1. a password passa por um sitio que nao e o IdP — historico da shell, `ps`, logs;
    2. o Keycloak nao consegue impor ACCOES OBRIGATORIAS (trocar a password a primeira entrada)
       nem MFA, porque nao ha ecra onde as apresentar;
    3. o ROPC esta removido do OAuth 2.1 — nao e uma preferencia, e um beco.

  Aqui a password so e escrita no ECRA DE LOGIN DO PROPRIO KEYCLOAK, no browser. Este script
  nunca a ve. Recebe apenas o codigo de autorizacao, e troca-o pelo token com o `code_verifier`
  que gerou — o PKCE garante que um codigo interceptado nao serve a mais ninguem.

  ⚠️ O ID-token vale UMA CHAMADA. O no impoe anti-replay por `jti`: reapresentar o mesmo token
  numa segunda leitura da 404, indistinguivel de "nao existe". Corra o script outra vez — a
  sessao do Keycloak persiste, portanto e um clique, nao uma password nova.

.EXAMPLE
  # so obter e inspeccionar as claims
  powershell -ExecutionPolicy Bypass -File deploy\server\get-id-token.ps1

.EXAMPLE
  # obter e LER um run (a prova que interessa)
  powershell -ExecutionPolicy Bypass -File deploy\server\get-id-token.ps1 -Run run-abc123

.EXAMPLE
  # a rota que atravessa TODOS os runs visiveis - a prova do anti-replay por `jti` (PR #96)
  powershell -ExecutionPolicy Bypass -File deploy\server\get-id-token.ps1 -Simular
#>
[CmdletBinding()]
param(
    [string]$Run     = "",
    # -Simular: em vez de ler UM run, chama a rota de simulacao de autonomia, que atravessa TODOS
    # os runs visiveis ao leitor. E a unica leitura que aplica a regra de residencia mais do que
    # uma vez na mesma chamada â foi ai que o replay de `jti` mordia (PR #96).
    [switch]$Simular,
    # -Cunhar <agente>: em vez de ler, autentica-se para CUNHAR uma NHI cuja raiz de delegacao e
    # o humano que se autenticar. A autenticacao fica LIGADA a esta delegacao pelo nonce.
    [string]$Cunhar  = "",
    [string]$Classe  = "agent-worker",
    [string]$Caps    = "model:invoke,cap:fs.read",
    [string]$Ttl     = "45m",
    [string]$Idp     = "https://aos.elysiumii.site:9443",
    [string]$No      = "https://aos.elysiumii.site:8444",
    [string]$Realm   = "aos",
    [string]$Cliente = "",
    [string]$CaPath  = "C:\Jimy\aos\deploy\server\secrets-local\internal-ca\ca.crt",
    [string]$Issuer  = "C:\Jimy\aos\packages\cmd\aos-issuer",
    [string]$Chave   = "C:\Jimy\aos\deploy\server\secrets-local\issuer.key",
    # TEM de constar dos redirectUris do cliente no Keycloak. Porta fixa e nao aleatoria porque
    # o Keycloak exige a correspondencia exacta do redirect_uri.
    [int]$Porta      = 47821,
    # -Submeter: depois de cunhar, submete UM run com esta NHI. Precisa de um SEGUNDO
    # login (audiencia aos-node) porque cunhar e chamar a API sao poderes distintos.
    [switch]$Submeter,
    [string]$Objectivo = "Le o documento 'notes' com a tool doc_read."
)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

function B64Url([byte[]]$b) {
    [Convert]::ToBase64String($b).TrimEnd('=').Replace('+','-').Replace('/','_')
}
function Aleatorio([int]$n) {
    $b = New-Object byte[] $n
    ([Security.Cryptography.RandomNumberGenerator]::Create()).GetBytes($b)
    return $b
}
function Claims([string]$jwt) {
    $p = $jwt.Split('.')[1].Replace('-','+').Replace('_','/')
    switch ($p.Length % 4) { 2 { $p += '==' } 3 { $p += '=' } }
    [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($p)) | ConvertFrom-Json
}

# ------------------------------------------------------------------------------------------
# TLS: o IdP e servido por um certificado da CA INTERNA, que nao esta na loja do Windows. Nao
# desligamos a validacao — FIXAMOS a ancora. A troca do codigo transporta uma credencial: aceitar
# qualquer certificado aqui abriria exactamente a porta que o resto do sistema fecha (quem se
# metesse no meio ficava com o ID-token do humano).
# ------------------------------------------------------------------------------------------
#
#  Em C# e nao num scriptblock por uma razao concreta: o .NET invoca este callback numa thread
#  de I/O que NAO tem runspace de PowerShell. Um scriptblock ali rebenta com "There is no
#  Runspace available to run scripts in this thread", e o sintoma que chega e enganador — um
#  generico "a ligacao subjacente foi fechada", que se confunde com um problema de rede.
#
if (-not (Test-Path $CaPath)) { throw "CA interna ausente em $CaPath" }
if (-not ([Management.Automation.PSTypeName]'AosPino').Type) {
    Add-Type -TypeDefinition @'
using System;
using System.Net;
using System.Net.Security;
using System.Security.Cryptography.X509Certificates;

public static class AosPino {
    public static X509Certificate2 Ca;

    public static bool Valida(object rem, X509Certificate cert, X509Chain cadeia, SslPolicyErrors erros) {
        if (erros == SslPolicyErrors.None) return true;          // ja confiavel pela loja do sistema
        // So o "nao conheco esta CA" e que este pino resolve. Nome errado, cert em falta ou
        // qualquer outro erro continuam a RECUSAR — senao um certificado que a nossa CA emitiu
        // para OUTRO servico (o vault, por exemplo) passaria por este endpoint.
        if (erros != SslPolicyErrors.RemoteCertificateChainErrors) return false;
        if (cert == null) return false;

        X509Chain c = new X509Chain();
        c.ChainPolicy.ExtraStore.Add(Ca);
        c.ChainPolicy.VerificationFlags = X509VerificationFlags.AllowUnknownCertificateAuthority;
        c.ChainPolicy.RevocationMode = X509RevocationMode.NoCheck;
        if (!c.Build(new X509Certificate2(cert))) return false;
        // A raiz da cadeia construida TEM de ser a NOSSA CA — nao basta a cadeia "validar",
        // porque AllowUnknownCertificateAuthority sozinho aceitaria qualquer auto-assinado.
        return c.ChainElements[c.ChainElements.Count - 1].Certificate.Thumbprint == Ca.Thumbprint;
    }

    public static void Instalar(string caminhoCa) {
        Ca = new X509Certificate2(caminhoCa);
        ServicePointManager.ServerCertificateValidationCallback = Valida;
    }
    public static void Remover() {
        ServicePointManager.ServerCertificateValidationCallback = null;
    }
}
'@
}
[AosPino]::Instalar($CaPath)
Write-Host ("CA interna fixada: {0}  [{1}]" -f [AosPino]::Ca.Subject, [AosPino]::Ca.Thumbprint) -ForegroundColor DarkGray

$redir = "http://127.0.0.1:$Porta/callback"
$base  = "$Idp/realms/$Realm/protocol/openid-connect"

# Obter-IdToken conduz UM fluxo completo de codigo+PKCE e devolve o ID-token.
#
# E funcao, e nao codigo em linha, porque o caminho de -Submeter precisa de DOIS tokens de
# AUDIENCIAS DIFERENTES: um para AUTORIZAR a delegacao (aos-issuer) e outro para CHAMAR a API
# (aos-node). Sao dois poderes distintos e e de proposito que um nao sirva para o outro. Com a
# sessao do Keycloak ja aberta, o segundo login e um clique e nao uma password nova.
function Obter-IdToken([string]$cliente, [string]$nonce) {
    $verifier  = B64Url (Aleatorio 64)
    $sha       = [Security.Cryptography.SHA256]::Create()
    $challenge = B64Url ($sha.ComputeHash([Text.Encoding]::ASCII.GetBytes($verifier)))
    $state     = B64Url (Aleatorio 24)

    $url = "$base/auth?client_id=$cliente&response_type=code&scope=openid" +
           "&redirect_uri=$([Uri]::EscapeDataString($redir))" +
           "&state=$state&nonce=$nonce&code_challenge=$challenge&code_challenge_method=S256"

    $ouvinte = New-Object Net.HttpListener
    $ouvinte.Prefixes.Add("http://127.0.0.1:$Porta/callback/")
    $ouvinte.Start()
    try {
        Write-Host ("`nA abrir o browser (cliente {0}). Autentique-se no ecra do Keycloak." -f $cliente) -ForegroundColor Cyan
        Write-Host "  (o certificado do IdP e da CA interna - o browser avisa; e esperado)`n" -ForegroundColor DarkGray
        Start-Process $url

        # Com limite de tempo: um GetContext() nu ficaria bloqueado para sempre se ninguem concluir.
        $async = $ouvinte.BeginGetContext($null, $null)
        if (-not $async.AsyncWaitHandle.WaitOne(300000)) {
            throw "sem resposta em 5 minutos - autenticacao nao concluida"
        }
        $ctx = $ouvinte.EndGetContext($async)
        $q   = $ctx.Request.QueryString

        $html = "<html><body style='font-family:sans-serif;padding:3rem'><h2>Pode fechar este separador.</h2><p>O ID-token foi entregue ao terminal.</p></body></html>"
        $buf  = [Text.Encoding]::UTF8.GetBytes($html)
        $ctx.Response.ContentType = 'text/html; charset=utf-8'
        $ctx.Response.OutputStream.Write($buf, 0, $buf.Length)
        $ctx.Response.Close()
    } finally { $ouvinte.Stop() }

    if ($q['error']) { throw "o IdP recusou: $($q['error']) - $($q['error_description'])" }
    # O `state` liga a resposta ao PEDIDO que este processo fez. Sem esta verificacao, um
    # atacante podia induzir a troca de um codigo dele pelo nosso verifier.
    if ($q['state'] -ne $state) { throw "state nao corresponde - resposta descartada" }
    $codigo = $q['code']
    if (-not $codigo) { throw "sem codigo de autorizacao na resposta" }

    $resp = Invoke-RestMethod -Method Post -Uri "$base/token" -Body @{
        grant_type    = 'authorization_code'
        code          = $codigo
        redirect_uri  = $redir
        client_id     = $cliente
        code_verifier = $verifier
    }
    if (-not $resp.id_token) { throw "a resposta do IdP nao trouxe id_token" }

    # O `nonce` liga o TOKEN ao nosso pedido - o `state` so liga o codigo. No caminho de -Cunhar
    # este nonce E o digest da delegacao, e a verificacao que o issuer faz e exactamente a mesma.
    $cl = Claims $resp.id_token
    if ($cl.nonce -ne $nonce) { throw "nonce nao corresponde - token descartado" }
    return $resp.id_token
}

function Mostrar-Claims([string]$tok, [bool]$eCunhagem) {
    $cl = Claims $tok
    Write-Host "ID-TOKEN OBTIDO" -ForegroundColor Green
    Write-Host ("  sub   = {0}" -f $cl.sub)
    Write-Host ("  user  = {0}" -f $cl.preferred_username)
    Write-Host ("  aud   = {0}" -f ($cl.aud -join ','))
    Write-Host ("  iss   = {0}" -f $cl.iss)
    Write-Host ("  jti   = {0}" -f $cl.jti)
    Write-Host ("  expira em {0}s" -f ([int]$cl.exp - [int]$cl.iat))
    if ($eCunhagem) {
        # A ausencia de `board` aqui e CORRECTA: cunhar nao e ler, e o cliente aos-issuer nao
        # traz o mapper de proposito - menos dados num token que nao precisa deles.
        Write-Host ("  nonce = {0}  (ligado a delegacao)" -f $cl.nonce) -ForegroundColor Green
    } else {
        Write-Host ("  board = {0}" -f $cl.board) -ForegroundColor $(if ($cl.board) { 'Green' } else { 'Red' })
        if (-not $cl.board) {
            Write-Host "`n  SEM claim 'board' - toda a leitura vai ser negada. O atributo do" -ForegroundColor Red
            Write-Host "  utilizador esta vazio, ou nao foi declarado no user profile do realm." -ForegroundColor Red
        }
    }
}

try {
    # AUDIENCIAS SEPARADAS de proposito: ler um run e cunhar uma raiz de delegacao sao dois
    # poderes distintos. Com uma audiencia so, um token obtido para LER servia para CUNHAR.
    if (-not $Cliente) { $Cliente = if ($Cunhar) { 'aos-issuer' } else { 'aos-node' } }

    if ($Cunhar) {
        # O nonce E o digest da delegacao, e vem do PROPRIO aos-issuer - nunca recalculado aqui.
        # Duas implementacoes do mesmo digest divergem, e a divergencia nao daria um erro legivel:
        # daria "nonce nao corresponde", que parece um ataque e seria um bug de portabilidade
        # (ordem dos campos, codificacao do texto, unidade do TTL).
        Push-Location $Issuer
        try   { $nonce = (& go run . delegation-nonce --agent $Cunhar --class $Classe --caps $Caps --ttl $Ttl 2>&1 | Select-Object -Last 1).ToString().Trim() }
        finally { Pop-Location }
        if (-not $nonce -or $nonce -match '\s') { throw "aos-issuer delegation-nonce falhou: $nonce" }

        Write-Host "`nDELEGACAO A AUTORIZAR" -ForegroundColor Cyan
        Write-Host ("  agente = {0}`n  classe = {1}`n  caps   = {2}`n  ttl    = {3}" -f $Cunhar,$Classe,$Caps,$Ttl)
        Write-Host ("  nonce  = {0}" -f $nonce) -ForegroundColor DarkGray
        Write-Host "  A sua autenticacao fica LIGADA a estes parametros: o token que sair daqui" -ForegroundColor DarkGray
        Write-Host "  nao serve para cunhar mais nada." -ForegroundColor DarkGray

        $tok = Obter-IdToken $Cliente $nonce
        Mostrar-Claims $tok $true

        Write-Host "`nA CUNHAR a NHI com a raiz de delegacao AUTENTICADA ..." -ForegroundColor Cyan
        # A assercao NUNCA vai a disco: entra por --assertion neste processo filho e morre com ele.
        Push-Location $Issuer
        try {
            $nhi = & go run . mint --key-file $Chave --issuer 'iss:aos-issuer' `
                --agent $Cunhar --class $Classe --caps $Caps --ttl $Ttl `
                --assertion $tok `
                --oidc-issuer "$Idp/realms/$Realm" --oidc-audience $Cliente --oidc-ca $CaPath 2>&1
        } finally { Pop-Location }
        $nhi = ($nhi | Select-Object -Last 1).ToString().Trim()
        if ($nhi -notmatch '^[A-Za-z0-9_.\-]+$') { throw "mint falhou: $nhi" }

        $b = $nhi.Split('.')[1].Replace('-','+').Replace('_','/')
        switch ($b.Length % 4) { 2 { $b += '==' } 3 { $b += '=' } }
        $p = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($b)) | ConvertFrom-Json
        $ligada = "$($p.auth_method)".StartsWith('oidc-bound:')
        Write-Host "NHI CUNHADA" -ForegroundColor Green
        Write-Host ("  agente      = {0}" -f $p.sub)
        Write-Host ("  auth_method = {0}" -f $p.auth_method) -ForegroundColor $(if ($ligada) { 'Green' } else { 'Yellow' })
        if ($p.delegation_chain) { Write-Host ("  raiz        = {0}" -f $p.delegation_chain[0].sub) }
        if (-not $ligada) {
            Write-Host "  ATENCAO: a delegacao NAO ficou ligada - o registo diz 'esteve presente'," -ForegroundColor Yellow
            Write-Host "  nao 'autorizou isto'." -ForegroundColor Yellow
        }

        if (-not $Submeter) {
            Write-Host "`n  A credencial (campo credential do POST /runs):" -ForegroundColor DarkGray
            Write-Host $nhi
            return
        }

        # SUBMETER com esta NHI. Precisa de um SEGUNDO token, de audiencia aos-node: cunhar a
        # delegacao e chamar a API sao poderes diferentes, e o token de um nao serve ao outro.
        $rid = "run-delegado-" + [int][double]::Parse((Get-Date -UFormat %s))
        $tokApi = Obter-IdToken 'aos-node' (B64Url (Aleatorio 24))
        Write-Host ("`nA SUBMETER {0} ..." -f $rid) -ForegroundColor Cyan
        $corpo = @{
            run_id        = $rid
            objective     = $Objectivo
            principal_nhi = $Cunhar
            credential    = $nhi
            scope         = @($Caps.Split(',') | Where-Object { $_ -like 'cap:*' })
        } | ConvertTo-Json -Compress
        try {
            $r = Invoke-WebRequest -Uri "$No/runs" -Method Post -UseBasicParsing `
                 -Headers @{ Authorization = "Bearer $tokApi" } -ContentType 'application/json' -Body $corpo
            Write-Host ("  HTTP {0}  {1}" -f $r.StatusCode, $r.Content) -ForegroundColor Green
            Write-Host ("`n  run_id = {0}" -f $rid) -ForegroundColor Green
        } catch {
            Write-Host ("  HTTP {0}" -f $_.Exception.Response.StatusCode.value__) -ForegroundColor Yellow
            $sr = New-Object IO.StreamReader($_.Exception.Response.GetResponseStream())
            Write-Host ("  {0}" -f $sr.ReadToEnd()); $sr.Close()
        }
        return
    }

    $tok = Obter-IdToken $Cliente (B64Url (Aleatorio 24))
    Mostrar-Claims $tok $false

    if ($Simular) {
        # A PROVA DO PR #96, e e uma prova que so producao pode dar.
        #
        # Esta rota atravessa TODOS os runs visiveis e aplicava a regra de residencia com uma
        # RE-VERIFICACAO da credencial por cada run distinto. O no impoe anti-replay por `jti`:
        # a segunda verificacao do MESMO token devolve replay. Resultado, antes da correccao:
        # `avaliados: 0` — com a credencial certa, sem erro e sem log.
        #
        # A bateria nao via isto porque compunha o guardiao de leitura com `cred = nil`, o
        # caminho legado por cabecalho, onde nao ha token para repetir.
        #
        # A PRIMEIRA VERSAO DESTE BLOCO ESTAVA ERRADA EM TRES PONTOS, e correu assim uma vez:
        # fazia GET em vez de POST, pedia `/v1/autonomy/simular` quando a rota e
        # `/autonomy/simular`, e lia um campo `mediacoes` que a resposta nao tem. O resultado
        # vazio que produziu nao era um facto sobre o no — era o script a nao lhe chegar. Fica
        # escrito porque uma prova que falha ao lado do alvo e pior do que nenhuma: parece
        # evidencia.
        #
        # COMO SE LE O RESULTADO:
        #   avaliados > 0                          -> a travessia por run NAO caiu em replay
        #   avaliados = 0 e ha runs mediados       -> a regressao voltou
        #   avaliados = 0 e nao ha runs mediados   -> INCONCLUSIVO; submeta um run que escale
        Write-Host "`nA SIMULAR autonomia como este humano ..." -ForegroundColor Cyan
        try {
            $r = Invoke-WebRequest -Uri "$No/autonomy/simular" -Method POST `
                -Headers @{ Authorization = "Bearer $tok" } `
                -ContentType "application/json" -Body '{"max":200}' -UseBasicParsing
            Write-Host ("  HTTP {0}" -f $r.StatusCode) -ForegroundColor Green
            Write-Host $r.Content
            try {
                $j = $r.Content | ConvertFrom-Json
                $n = [int]$j.avaliados
                if ($n -gt 0) {
                    Write-Host ("  avaliados={0}  correriam={1}  escalariam={2}" -f $n, $j.correriam, $j.escalariam) -ForegroundColor Green
                    Write-Host "  A travessia por run NAO caiu em replay — o PR #96 esta vivo neste no." -ForegroundColor Green
                } else {
                    Write-Host "  avaliados=0. Isto e INCONCLUSIVO, nao e prova de avaria:" -ForegroundColor Yellow
                    Write-Host "    - se nao houve tool calls seladas, zero e a resposta CERTA;" -ForegroundColor DarkGray
                    Write-Host "    - se houve, entao a travessia por run voltou a cair em replay." -ForegroundColor DarkGray
                    Write-Host "  Submeta um run com tool calls e volte a correr (o token gasta-se: obtenha outro)." -ForegroundColor DarkGray
                }
            } catch {
                Write-Host "  (resposta nao e o JSON esperado — leia o corpo acima)" -ForegroundColor DarkGray
            }
        } catch {
            $cod = $_.Exception.Response.StatusCode.value__
            Write-Host ("  HTTP {0}" -f $cod) -ForegroundColor Yellow
            switch ($cod) {
                403 { Write-Host "  403: a credencial nao foi aceite. Se ja usou este token, ESTA GASTO (anti-replay por jti)." -ForegroundColor DarkGray }
                404 { Write-Host "  404: rota inexistente neste no — confirme o path (/autonomy/simular, sem /v1)." -ForegroundColor DarkGray }
                405 { Write-Host "  405: metodo errado — esta rota e POST." -ForegroundColor DarkGray }
                501 { Write-Host "  501: governanca soberana ou WORM nao compostos neste no." -ForegroundColor DarkGray }
            }
        }
    } elseif ($Run) {
        Write-Host "`nA LER $Run como este humano ..." -ForegroundColor Cyan
        # O no em :8444 tem certificado publico (Let's Encrypt) - validacao normal, sem fixacao.
        try {
            $r = Invoke-WebRequest -Uri "$No/runs/$Run" -Headers @{ Authorization = "Bearer $tok" } -UseBasicParsing
            Write-Host ("  HTTP {0}" -f $r.StatusCode) -ForegroundColor Green
            Write-Host $r.Content
        } catch {
            $cod = $_.Exception.Response.StatusCode.value__
            Write-Host ("  HTTP {0}" -f $cod) -ForegroundColor Yellow
            if ($cod -eq 404) {
                Write-Host "  404 e UNIFORME: 'nao existe' e 'existe noutra regiao' sao indistinguiveis." -ForegroundColor DarkGray
                Write-Host "  E deliberado - enumerar runs de outra soberania seria a fuga." -ForegroundColor DarkGray
            }
        }
    } else {
        Write-Host "`n(sem -Run nem -Simular: token obtido e descartado, nao foi gravado em lado nenhum)" -ForegroundColor DarkGray
    }
}
finally {
    [AosPino]::Remover()
}
