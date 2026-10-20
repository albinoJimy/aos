package main

// O GATE DE TAINT PEDE APROVAÇÃO HUMANA EM VEZ DE NEGAR (AOS-541, passo 3 do plano do dono de
// 2026-10-19 para o limite do taint) — `AOS_TAINT_ESCALA`.
//
// Depois do AOS-538 (ler fica livre) e do AOS-540 (pedir antes de ler) sobra um caso: com o
// contexto `untrusted`, o agente quer mesmo uma tool privilegiada que envia ou muda. O gate de taint
// nega e o run falha.
//
//   - `off` (por omissão): nada muda — o gate nega, e os eventos, o prompt e o `/metrics` são os de
//     antes, byte a byte.
//   - `on`: o gate devolve «escalar» em vez de «negar». O run pára em `waiting_on_human`, o
//     pendente aparece ao operador com `escalated_by: "taint"`, e a cerimónia tem de ser de QUATRO
//     OLHOS (o nó recusa uma de uma perna para estas escaladas). Aprovada, a retoma corre ESSA call
//     — a preview amarra o run, o passo, a tool, a capability, o recurso resolvido, o principal e o
//     input. Expirada (ou decidida sem aprovação), a retoma faz o run FALHAR com a causa nomeada
//     ([ErrAprovacaoDoTaintExpirada], [ErrAprovacaoDoTaintRecusada]) em vez de voltar a suspender.
//
// O que NÃO muda com `on`: a leitura local do AOS-538 continua a passar sem escalar; o rótulo de
// taint não muda com a aprovação (a call aprovada fica `untrusted` no evento, com
// `human_approved`); e a aprovação vale só para aquela call — uma segunda call igual noutro passo
// tem outra preview e volta a escalar, e o grant é de uso único.
//
// SEM APROVADORES, O NÓ RECUSA ARRANCAR ([ErrTaintEscalaSemAprovadores]). As alternativas eram
// piores: compor a escalada sem quem a decida deixava runs suspensos para sempre (ou, sem destino de
// escalada, a escalada degradava para negação com o operador convencido do contrário); manter a
// negação e dizê-lo no banner era um interruptor `on` que não faz nada, que é o mesmo engano com
// uma linha de log a desculpá-lo. Recusar o arranque põe a contradição à frente de quem a escreveu.
//
// RECUO: voltar a `off`. Um run suspenso por uma escalada do taint continua suspenso; retomado com
// `off`, a call é negada como antes.

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Os dois valores de AOS_TAINT_ESCALA.
const (
	taintEscalaOff = "off"
	taintEscalaOn  = "on"
)

// ErrBadTaintEscala — AOS_TAINT_ESCALA com um valor fora do vocabulário. O nó recusa arrancar: um
// valor mal escrito não pode cair em silêncio para `off` nem para `on`.
var ErrBadTaintEscala = errors.New("aos: AOS_TAINT_ESCALA invalida — valores aceites: off (por omissao; o gate de taint nega a tool call privilegiada com o contexto untrusted, como sempre) e on (o gate escala-a para aprovacao humana de quatro olhos; exige AOS_APPROVERS_FILE)")

// ErrTaintEscalaSemAprovadores — AOS_TAINT_ESCALA=on sem o ciclo de aprovação composto (four-eyes
// de AOS_APPROVERS_FILE, registo de pendentes e broker de grants). Fail-closed: ver o cabeçalho.
var ErrTaintEscalaSemAprovadores = errors.New("aos: AOS_TAINT_ESCALA=on exige aprovadores (AOS_APPROVERS_FILE): sem o ciclo four-eyes composto ninguem decide uma escalada do gate de taint e o run ficaria suspenso para sempre — defina AOS_APPROVERS_FILE ou volte a AOS_TAINT_ESCALA=off (AOS-541)")

// ErrAprovacaoDoTaintExpirada — a retoma voltou a escalar, pelo gate de taint, uma call cujo
// pendente expirou sem decisão. O run falha com esta causa em vez de voltar a suspender.
var ErrAprovacaoDoTaintExpirada = errors.New("aos: a aprovacao humana da tool call escalada pelo gate de taint EXPIROU sem decisao — o run falha (AOS-541)")

// ErrAprovacaoDoTaintRecusada — a retoma voltou a escalar, pelo gate de taint, uma call cujo
// pendente foi decidido por um humano sem aprovação. O run falha com esta causa.
var ErrAprovacaoDoTaintRecusada = errors.New("aos: a aprovacao humana da tool call escalada pelo gate de taint foi RECUSADA — o run falha (AOS-541)")

// parseTaintEscalaFromEnv lê AOS_TAINT_ESCALA. Vazia ⇒ false (`off`). Sem normalização de caixa,
// pela regra das outras variáveis de vocabulário fechado do nó.
func parseTaintEscalaFromEnv() (bool, error) {
	switch raw := strings.TrimSpace(os.Getenv("AOS_TAINT_ESCALA")); raw {
	case "", taintEscalaOff:
		return false, nil
	case taintEscalaOn:
		return true, nil
	default:
		return false, fmt.Errorf("%w (veio %q)", ErrBadTaintEscala, raw)
	}
}

// taintEscalaBanner é a linha de postura do interruptor, do estado COMPOSTO: `ligado` é o valor que
// chegou à cadeia e `gateActivo` o predicado de eficácia do RM. Com `off` não sai linha nenhuma: o
// arranque é o de antes.
func taintEscalaBanner(ligado, gateActivo bool) []string {
	if !ligado {
		return nil
	}
	linha := "taint / escalada para aprovacao humana (AOS-541): AOS_TAINT_ESCALA=on — uma tool call PRIVILEGIADA pedida com o contexto untrusted ja nao e negada: o run para em waiting_on_human e o pendente (escalated_by=taint) exige uma cerimonia de QUATRO OLHOS; aprovada, a retoma corre ESSA call (a preview amarra tool, capability, recurso resolvido, principal e input) e o evento diz untrusted com human_approved; expirada ou recusada, a retoma faz o run FALHAR com a causa nomeada. A leitura local (AOS-538) continua a passar sem escalar; o rotulo de taint nao muda; a aprovacao vale so para aquela call (uma segunda igual volta a escalar). O evento de mediacao diz quem escalou: tool.call.escalated com denied_by=taint e context.taint_privilege. Recuo: off"
	if !gateActivo {
		linha += " — ATENCAO: o gate de taint esta INERTE (AOS_PRIVILEGED_CAPS vazia), logo nada e privilegiado e NADA escala; o interruptor so tem efeito com AOS_PRIVILEGED_CAPS definida"
	}
	return []string{linha}
}
