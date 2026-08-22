<#
.SYNOPSIS
  Invólucro da selagem diária: corre o `selar-worm.ps1`, guarda a saída, e propaga o código.

.DESCRIPTION
  PORQUE EXISTE, e não é comodidade. O `schtasks` pode invocar o `selar-worm.ps1` directamente —
  é o que o README documentava — mas nesse caso a saída vai para lado nenhum: o que sobra de uma
  falha diária é uma coluna «Last Result» com um número. A selagem toca no WORM de produção, traz
  material por SSH e entrega uma âncora; quando falhar, quem a for ler quer saber PORQUÊ, e não
  vai reproduzir o problema à mão às 03:30 do dia seguinte.

  Um erro diário que ninguém lê é um erro que não existe.

  O QUE ESTE INVÓLUCRO NÃO FAZ, e é deliberado: não decide nada, não repete, não engole. Se a
  selagem falhar, o código de saída sobe INTACTO para o agendador — a tarefa fica vermelha. Um
  invólucro que normalizasse o código para 0 transformaria um agendador honesto num que mente,
  que é exactamente o defeito que a selagem existe para tornar impossível na cadeia.
#>
[CmdletBinding()]
param(
    [string]$Logs      = "$env:USERPROFILE\aos-selagem-logs",
    # 30 dias: mais do que a janela em que alguém repara que a cadência parou, menos do que o
    # necessário para o directório crescer sem ninguém dar por isso.
    [int]$DiasDeLog    = 30,
    # Repassados ao script real, para que a tarefa possa apontar a outra máquina sem editar isto.
    [string]$Servidor  = "aos@37.60.241.150",
    [string]$ChaveSSH  = "C:\Jimy\aos\deploy\server\secrets-local\deploy_key",
    [string]$KnownHosts = "C:\Jimy\aos\deploy\server\secrets-local\known_hosts.txt",
    # -Script existe para os TESTES do próprio invólucro: sem ele, a única forma de verificar que
    # o código de saída sobe intacto era correr uma selagem real contra produção.
    [string]$Script    = ""
)

# 'Continue' e NÃO 'Stop': este invólucro tem de chegar ao fim para escrever o rodapé do log e
# podar os antigos, mesmo — sobretudo — quando o que ele envolve falha.
$ErrorActionPreference = 'Continue'

if (-not $Script) { $Script = Join-Path $PSScriptRoot 'selar-worm.ps1' }
New-Item -ItemType Directory -Path $Logs -Force | Out-Null
$carimbo = Get-Date -Format 'yyyy-MM-dd-HHmmss'
$log = Join-Path $Logs "selagem-$carimbo.log"

"==== selagem WORM — inicio $(Get-Date -Format 'o') ====" | Out-File -FilePath $log -Encoding utf8

# Processo FILHO e não dot-source: é a forma de ter um `$LASTEXITCODE` que significa mesmo o
# desfecho do script, incluindo quando ele morre por excepção não apanhada.
& powershell.exe -NoProfile -ExecutionPolicy Bypass -File $Script `
    -PorSSH -Entregar -Servidor $Servidor -ChaveSSH $ChaveSSH -KnownHosts $KnownHosts `
    *>&1 | ForEach-Object {
        # NÃO `Tee-Object`: o do PowerShell 5.1 escreve UTF-16 e não aceita `-Encoding`, pelo que
        # o log saía com o cabeçalho em UTF-8 e o corpo em UTF-16 — duas codificações no mesmo
        # ficheiro. Um `grep` sobre isso não encontra nada e ninguém percebe porquê.
        #
        # Abre e fecha por linha, o que é lento e é o preço de STREAMAR: uma tarefa que fique
        # presa no ssh e seja morta perde tudo o que estivesse em buffer, e é precisamente nessa
        # que se quer ler até onde chegou.
        Write-Host $_
        $_ | Out-File -FilePath $log -Append -Encoding utf8
    }
$codigo = $LASTEXITCODE

"---- fim $(Get-Date -Format 'o') — codigo $codigo ----" | Out-File -FilePath $log -Append -Encoding utf8

# ÚLTIMO DESFECHO num ficheiro de nome FIXO: quem quer saber se a cadência está viva não deve ter
# de ordenar um directório por data. Contém o código e o caminho do log da vez.
"$carimbo codigo=$codigo log=$log" |
    Out-File -FilePath (Join-Path $Logs 'ULTIMA.txt') -Encoding utf8

Get-ChildItem -Path $Logs -Filter 'selagem-*.log' -ErrorAction SilentlyContinue |
    Where-Object { $_.LastWriteTime -lt (Get-Date).AddDays(-$DiasDeLog) } |
    Remove-Item -Force -ErrorAction SilentlyContinue

if ($codigo -ne 0) {
    Write-Host "SELAGEM FALHOU (codigo $codigo). Log: $log" -ForegroundColor Red
}
exit $codigo
