package main

// AUDIT DE GOVERNAÇÃO DO MODEL GATEWAY (EPIC-06/AOS-265) — STORE DURÁVEL.
//
// O Model Gateway sela no seu [audit.Store] a governação da soberania: o changelog
// de ACTIVAÇÃO da allowlist regional (verificada+assinada) no arranque e cada decisão
// allow/deny por chamada (incl. o deny de failover cross-border). Até AOS-265 esse
// store era SEMPRE um [audit.NewMemStore] (ver o call site em modelgatewaywiring.go):
// a governação do gateway NÃO sobrevivia a um restart — a activação selada e o trilho
// de decisões evaporavam, e nada o declarava.
//
// AOS-265 troca-o pela CADEIA DURÁVEL quando AOS_MODEL_AUDIT_PATH está definida: um
// [audit.FileStore] WORM tamper-evident em disco (hash-chain, mesmo motor do WORM do
// nó, AOS-170). Vazio ⇒ mantém o [audit.NewMemStore] (comportamento inalterado), e o
// banner DECLARA que a governação do gateway é volátil — nunca silêncio.
//
// PORQUÊ UM STORE DEDICADO E NÃO O WORM DO NÓ. O cliente de modelo é construído em
// [parseModelFromEnv], ANTES de [Bootstrap] compor o WORM do nó (que abre/verifica a
// hash-chain em AOS-221) — a ordem de construção não deixa o WORM do nó disponível
// aqui. Um segundo handle de FileStore sobre o MESMO ficheiro seria dois escritores no
// mesmo WAL append-only (corrupção/lock). Por isso a governação do gateway sela na SUA
// cadeia durável dedicada; PARTILHAR o WORM único do nó exigiria construir o gateway
// DENTRO de Bootstrap (re-ordenação da composição do modelo) e fica DECLARADAMENTE
// deferido — o ganho de AOS-265 é a DURABILIDADE (a governação sobrevive ao restart),
// que este store entrega.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	audit "github.com/aos-ref/platform/audit"
)

// ErrBadModelAudit — AOS_MODEL_AUDIT_PATH está definida mas o WORM durável não abre
// (caminho inacessível, WAL adulterado que o replay recusa). Fail-closed de CONFIG,
// no molde de [ErrBadBrokerVault]: distingue "não configurado" (vazio ⇒ MemStore) de
// "configurado mas inválido" (aborta o arranque em vez de degradar em silêncio para
// governação volátil).
var ErrBadModelAudit = errors.New("aos: audit durável do model gateway mal configurado — AOS_MODEL_AUDIT_PATH aponta um WORM que não abre (caminho inacessível ou hash-chain adulterada que o replay recusa); fail-closed em vez de degradar para audit in-memory silencioso")

// parseModelAuditFromEnv resolve o [audit.Store] de governação do Model Gateway a
// partir do ambiente (AOS-265). Vazio ⇒ (nil, "", nil): o call site usa o
// [audit.NewMemStore] de referência (governação VOLÁTIL, declarada no banner).
// Presente ⇒ abre o WORM durável ([audit.OpenFileStore], crash-safe: replay + reabre
// em append) e devolve-o para o call site o injectar no gateway. Fail-closed
// ([ErrBadModelAudit]).
//
// O store fica aberto durante a vida do nó (é o audit do gateway); o descritor é
// libertado no encerramento do processo — não há um Close por-pedido, tal como o WORM
// do nó não o tem no caminho durável.
//
// Env:
//   - AOS_MODEL_AUDIT_PATH — caminho do WAL do WORM durável de governação do gateway.
//     Vazio ⇒ MemStore (inalterado).
func parseModelAuditFromEnv() (audit.Store, string, error) {
	path := strings.TrimSpace(os.Getenv("AOS_MODEL_AUDIT_PATH"))
	if path == "" {
		return nil, "", nil // não configurado ⇒ MemStore de referência (governação volátil).
	}
	fs, err := audit.OpenFileStore(path)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrBadModelAudit, err)
	}
	return fs, path, nil
}

// modelAuditPostureBanner declara o MODO do audit de governação do Model Gateway
// (AOS-265), amarrado ao ESTADO composto (o store REALMENTE durável), não à intenção
// da config — a disciplina de banner de AOS-203. Só sai quando há um gateway composto
// (gatewayComposed): sem gateway não há audit de governação a declarar.
func modelAuditPostureBanner(gatewayComposed bool, durablePath string) []string {
	if !gatewayComposed {
		return nil
	}
	if durablePath == "" {
		return []string{
			"audit de governacao do model gateway (EPIC-06/AOS-265): IN-MEMORY (VOLATIL) — a activacao selada da allowlist regional e o trilho de decisoes allow/deny por chamada NAO sobrevivem a um restart. Defina AOS_MODEL_AUDIT_PATH para selar a governacao do gateway num WORM DURAVEL tamper-evident (hash-chain). Eixo: AOS-265",
		}
	}
	return []string{
		fmt.Sprintf("audit de governacao do model gateway (EPIC-06/AOS-265): DURAVEL — governacao selada num WORM tamper-evident em disco (%s); a activacao da allowlist e as decisoes allow/deny por chamada sobrevivem ao restart (replay crash-safe). Cadeia DEDICADA do gateway (a partilha do WORM unico do no fica deferida — o cliente de modelo compoe-se antes do WORM do no). Eixo: AOS-265", durablePath),
	}
}
