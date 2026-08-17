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
#>
[CmdletBinding()]
param(
    [string]$Run     = "",
    [string]$Idp     = "https://aos.elysiumii.site:9443",
    [string]$No      = "https://aos.elysiumii.site:8444",
    [string]$Realm   = "aos",
    [string]$Cliente = "aos-node",
    [string]$CaPath  = "C:\Jimy\aos\deploy\server\secrets-local\internal-ca\ca.crt",
    # TEM de constar dos redirectUris do cliente no Keycloak. Porta fixa e nao aleatoria porque
    # o Keycloak exige a correspondencia exacta do redirect_uri.
    [int]$Porta      = 47821
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

try {
    $redir  = "http://127.0.0.1:$Porta/callback"
    $base   = "$Idp/realms/$Realm/protocol/openid-connect"

    $verifier  = B64Url (Aleatorio 64)
    $sha       = [Security.Cryptography.SHA256]::Create()
    $challenge = B64Url ($sha.ComputeHash([Text.Encoding]::ASCII.GetBytes($verifier)))
    $state     = B64Url (Aleatorio 24)
    $nonce     = B64Url (Aleatorio 24)

    $url = "$base/auth?client_id=$Cliente&response_type=code&scope=openid" +
           "&redirect_uri=$([Uri]::EscapeDataString($redir))" +
           "&state=$state&nonce=$nonce&code_challenge=$challenge&code_challenge_method=S256"

    $ouvinte = New-Object Net.HttpListener
    $ouvinte.Prefixes.Add("http://127.0.0.1:$Porta/callback/")
    $ouvinte.Start()
    Write-Host "`nA abrir o browser. Autentique-se no ecra do Keycloak." -ForegroundColor Cyan
    Write-Host "  (o certificado do IdP e da CA interna — o browser avisa; e esperado)`n" -ForegroundColor DarkGray
    Start-Process $url

    # Com limite de tempo: um GetContext() nu ficaria bloqueado para sempre se ninguem concluir.
    $async = $ouvinte.BeginGetContext($null, $null)
    if (-not $async.AsyncWaitHandle.WaitOne(300000)) {
        $ouvinte.Stop(); throw "sem resposta em 5 minutos — autenticacao nao concluida"
    }
    $ctx = $ouvinte.EndGetContext($async)
    $q   = $ctx.Request.QueryString

    $html = "<html><body style='font-family:sans-serif;padding:3rem'><h2>Pode fechar este separador.</h2><p>O ID-token foi entregue ao terminal.</p></body></html>"
    $buf  = [Text.Encoding]::UTF8.GetBytes($html)
    $ctx.Response.ContentType = 'text/html; charset=utf-8'
    $ctx.Response.OutputStream.Write($buf, 0, $buf.Length)
    $ctx.Response.Close()
    $ouvinte.Stop()

    if ($q['error']) { throw "o IdP recusou: $($q['error']) — $($q['error_description'])" }
    # O `state` liga a resposta ao PEDIDO que este processo fez. Sem esta verificacao, um
    # atacante podia induzir a troca de um codigo dele pelo nosso verifier.
    if ($q['state'] -ne $state) { throw "state nao corresponde — resposta descartada" }
    $codigo = $q['code']
    if (-not $codigo) { throw "sem codigo de autorizacao na resposta" }

    $resp = Invoke-RestMethod -Method Post -Uri "$base/token" -Body @{
        grant_type    = 'authorization_code'
        code          = $codigo
        redirect_uri  = $redir
        client_id     = $Cliente
        code_verifier = $verifier
    }
    if (-not $resp.id_token) { throw "a resposta do IdP nao trouxe id_token" }

    $cl = Claims $resp.id_token
    # O `nonce` liga o TOKEN ao nosso pedido — o `state` so liga o codigo.
    if ($cl.nonce -ne $nonce) { throw "nonce nao corresponde — token descartado" }

    Write-Host "ID-TOKEN OBTIDO" -ForegroundColor Green
    Write-Host ("  sub   = {0}" -f $cl.sub)
    Write-Host ("  user  = {0}" -f $cl.preferred_username)
    Write-Host ("  board = {0}" -f $cl.board) -ForegroundColor $(if ($cl.board) { 'Green' } else { 'Red' })
    Write-Host ("  aud   = {0}" -f ($cl.aud -join ','))
    Write-Host ("  iss   = {0}" -f $cl.iss)
    Write-Host ("  jti   = {0}" -f $cl.jti)
    Write-Host ("  expira em {0}s" -f ([int]$cl.exp - [int]$cl.iat))
    if (-not $cl.board) {
        Write-Host "`n  SEM claim 'board' — toda a leitura vai ser negada. O atributo do" -ForegroundColor Red
        Write-Host "  utilizador esta vazio, ou nao foi declarado no user profile do realm." -ForegroundColor Red
    }

    if ($Run) {
        Write-Host "`nA LER $Run como este humano ..." -ForegroundColor Cyan
        # O no em :8444 tem certificado publico (Let's Encrypt) — validacao normal, sem fixacao.
        try {
            $r = Invoke-WebRequest -Uri "$No/runs/$Run" -Headers @{ Authorization = "Bearer $($resp.id_token)" } -UseBasicParsing
            Write-Host ("  HTTP {0}" -f $r.StatusCode) -ForegroundColor Green
            Write-Host $r.Content
        } catch {
            $cod = $_.Exception.Response.StatusCode.value__
            Write-Host ("  HTTP {0}" -f $cod) -ForegroundColor Yellow
            if ($cod -eq 404) {
                Write-Host "  404 e UNIFORME: 'nao existe' e 'existe noutra regiao' sao indistinguiveis." -ForegroundColor DarkGray
                Write-Host "  E deliberado — enumerar runs de outra soberania seria a fuga." -ForegroundColor DarkGray
            }
        }
    } else {
        Write-Host "`n(sem -Run: token obtido e descartado, nao foi gravado em lado nenhum)" -ForegroundColor DarkGray
    }
}
finally {
    [AosPino]::Remover()
}
