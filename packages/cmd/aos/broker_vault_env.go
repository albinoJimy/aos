package main

// CUSTÓDIA DO CREDENTIAL BROKER (AOS-070/AOS-264) — cliente/token Vault SEPARADOS.
//
// Decisão do dono D7 (2026-08-10): o Vault de credenciais downstream do broker tem
// cliente e token PRÓPRIOS (AOS_BROKER_VAULT_*), DISTINTOS do Vault da custódia da
// KEK (AOS_DSAR_VAULT_*, motor Transit key-never-leaves que RECUSA devolver
// material). Dois eixos de autoridade que não se devem partilhar: um só token que
// acumulasse "destruir chaves Transit" (crypto-shred) e "ler credenciais downstream"
// seria over-privilege e um único comprometimento daria exfiltração E over-erasure.
//
// ÂMBITO DESTA ENTREGA (AOS-264 é PREPARAÇÃO): esta função PREPARA o cliente Vault
// REAL (KV v2) e valida a config fail-closed, mas NÃO liga a troca mediada ao
// gateway. O banner declara-o com honestidade: "configurado, troca PENDENTE", nunca
// "broker ligado".
//
// CORRECÇÃO (AOS-325): esta nota apontava a pendência para AOS-265. O AOS-265 JÁ
// ATERROU — a porta de aquisição in-process existe em `platform/broker/inprocess.go`,
// com testes — e NÃO ligou a troca. O bloqueador real é o DEF-218 (bundle PDP assinado
// + identidade de infra com a capability da troca). Apontar um ticket já fechado é pior
// do que não apontar nenhum: o operador conclui que a pendência tem dono e data.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aos-ref/integration"
	broker "github.com/aos-ref/platform/broker"
)

// ErrBadBrokerVault — o Vault de credenciais downstream do broker (AOS-264) está
// pedido (AOS_BROKER_VAULT_ADDR presente) mas mal configurado: sem o token por
// FICHEIRO montado, ou o ficheiro é ilegível/vazio. Fail-closed de CONFIG, no molde
// de [ErrBadVaultDSAR]: distingue "não configurado" (addr vazio ⇒ dormente) de
// "configurado mas inválido" (aborta, em vez de degradar em silêncio).
// ErrInsecureBrokerVaultAddr — AOS_BROKER_VAULT_ADDR com transporte inseguro (AOS-323).
//
// A ASSIMETRIA QUE ISTO FECHA. O Vault da KEK já recusava texto-claro desde AOS-249 —
// [ErrInsecureVaultDSARAddr], via [integration.CheckSecureTransportURL], que exige https e só
// admite http em LOOPBACK. O Vault do broker, endereçado pela mesma família de variáveis e a
// transportar o mesmo tipo de material, não validava nada: o mesmo binário recusava num sítio o
// que aceitava no outro. Usa-se o MESMO helper de propósito — um segundo critério, ainda que
// equivalente hoje, divergiria à primeira alteração.
//
// O `X-Vault-Token` viaja em cada pedido; sobre http não-loopback viaja em claro, e quem o
// apanhar colhe as credenciais downstream que o broker custodia.
var ErrInsecureBrokerVaultAddr = errors.New("aos: AOS_BROKER_VAULT_ADDR com transporte INSEGURO — o token do Vault e as credenciais downstream atravessariam a rede em claro; exige https (http SO em loopback), o mesmo criterio de AOS_DSAR_VAULT_ADDR no mesmo binario")

var ErrBadBrokerVault = errors.New("aos: Vault do credential broker mal configurado — AOS_BROKER_VAULT_ADDR exige AOS_BROKER_VAULT_TOKEN_PATH (ficheiro montado com o token do Vault; material privado NUNCA por variável de ambiente)")

// brokerVaultSettings é o material PÚBLICO do broker Vault que o banner declara (uma
// URL, um nome de mount). NÃO transporta o token nem qualquer segredo.
type brokerVaultSettings struct {
	Addr    string // URL do Vault do broker (público)
	KVMount string // mount do motor KV v2 (público)
}

// parseBrokerVaultFromEnv PREPARA o cliente Vault REAL (KV v2) do credential broker
// a partir do ambiente (AOS-264, decisão D7). Vazio ⇒ (nil, nil, nil): não
// configurado (dormente). Presente ⇒ exige o token por FICHEIRO montado (material
// privado nunca por env, no padrão de AOS_DSAR_VAULT_TOKEN_PATH) e constrói o
// cliente por detrás da porta [broker.VaultClient] — provando que compõe ao arranque
// (fail-closed). O cliente é ENCAMINHADO para [Config.BrokerVault]; a troca mediada
// consumi-lo-á quando existir — hoje NENHUM `Fetch` é emitido, e o cliente alimenta
// apenas a linha de postura. Bloqueador: DEF-218 (AOS-265 já aterrou sem o fechar).
//
// Envs:
//   - AOS_BROKER_VAULT_ADDR       — URL do Vault (público). Vazio ⇒ dormente.
//   - AOS_BROKER_VAULT_TOKEN_PATH — ficheiro montado com o token (privado). Obrigatório se ADDR.
//   - AOS_BROKER_VAULT_KV_MOUNT   — mount do KV v2 (default "secret"). Público.
//   - AOS_BROKER_VAULT_PATH_PREFIX— prefixo de path opcional (namespacing). Público.
func parseBrokerVaultFromEnv() (broker.VaultClient, *brokerVaultSettings, error) {
	addr := strings.TrimSpace(os.Getenv("AOS_BROKER_VAULT_ADDR"))
	if addr == "" {
		return nil, nil, nil // não configurado ⇒ broker Vault DORMENTE (comportamento actual).
	}
	// TRANSPORTE (AOS-323): mesmo critério do Vault da KEK, mesmo helper. Antes de ler o token —
	// não faz sentido validar credenciais para um endereço que não se vai aceitar.
	if err := integration.CheckSecureTransportURL(addr); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInsecureBrokerVaultAddr, err)
	}
	tokenPath := strings.TrimSpace(os.Getenv("AOS_BROKER_VAULT_TOKEN_PATH"))
	if tokenPath == "" {
		return nil, nil, ErrBadBrokerVault
	}
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: ler token: %v", ErrBadBrokerVault, err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return nil, nil, fmt.Errorf("%w: token vazio em %q", ErrBadBrokerVault, tokenPath)
	}
	mount := strings.TrimSpace(os.Getenv("AOS_BROKER_VAULT_KV_MOUNT"))
	if mount == "" {
		mount = "secret"
	}
	prefix := strings.TrimSpace(os.Getenv("AOS_BROKER_VAULT_PATH_PREFIX"))

	client, err := broker.NewVaultKVv2(broker.VaultKVv2Config{
		Addr:   addr,
		Token:  token,
		Mount:  mount,
		Prefix: prefix,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrBadBrokerVault, err)
	}
	return client, &brokerVaultSettings{Addr: addr, KVMount: mount}, nil
}

// brokerVaultPostureBanner declara o MODO do Vault de credenciais downstream do
// broker (AOS-264), amarrado ao ESTADO composto (o cliente REALMENTE preparado),
// não à intenção da config — a mesma disciplina de [credentialBrokerPostureBanner] e
// das restantes linhas de postura.
//
// A regra da linha 19 de posture_banner.go aplicada aqui: quando CONFIGURADO, a
// linha diz "configurado" E "troca PENDENTE" — porque anunciar "broker
// LIGADO" sobre um Vault preparado mas cuja troca ainda não medeia nada seria a
// promessa a mais que treina o operador a confiar numa protecção inexistente.
func brokerVaultPostureBanner(s *brokerVaultSettings) []string {
	if s == nil {
		return []string{
			"broker vault / credenciais downstream (AOS-070/AOS-264): DORMENTE — AOS_BROKER_VAULT_ADDR nao esta definida, logo NAO ha custodia externa de credenciais downstream para a troca mediada. Defina AOS_BROKER_VAULT_ADDR + AOS_BROKER_VAULT_TOKEN_PATH (cliente/token SEPARADOS do AOS_DSAR_VAULT_*, decisao D7) para PREPARAR o cliente Vault REAL (KV v2). Eixo: AOS-264; bloqueador da troca: DEF-218",
		}
	}
	return []string{
		fmt.Sprintf("broker vault / credenciais downstream (AOS-070/AOS-264): CONFIGURADO (KV v2 @ %s, mount %q), troca PENDENTE — cliente Vault REAL preparado (stdlib, zero-dep), token de FICHEIRO montado, SEPARADO do Vault da KEK (D7). ATENCAO: a TROCA MEDIADA continua PENDENTE, ainda NAO esta ligada ao gateway nesta entrega — o cliente esta PREPARADO e NENHUM Fetch e emitido — alimenta apenas esta linha. A porta de aquisicao in-process (AOS-265, D8) JA EXISTE em platform/broker; o que falta e a COMPOSICAO da troca, cujo bloqueador e o DEF-218 (bundle PDP assinado + identidade de infra com a capability da troca). Ate la NENHUMA credencial downstream e trocada nem injectada. DECISAO REGISTADA: motor KV v2 (segredo estatico) — a lease do broker corta a INJECCAO in-process no TTL/revogacao, mas so DYNAMIC SECRETS dariam corte da credencial NO provedor (deferido, D8-B/DEF-216). TRANSPORTE: https exigido, http SO em loopback — mesmo criterio e mesmo helper do AOS_DSAR_VAULT_ADDR (AOS-323). TOKEN: lido UMA VEZ do ficheiro montado no arranque e NUNCA RENOVADO — ao contrario do Vault da KEK, que sonda e renova (AOS-249). Enquanto nenhum Fetch e emitido isto nao tem efeito; no dia do wiring (DEF-218) o token tem de passar a ser renovado ou a ter validade maior que a vida do processo. Eixo: DEF-218", integration.RedactURL(s.Addr), s.KVMount),
	}
}
