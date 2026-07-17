package breaker

import "errors"

var (
	// ErrNilMachine — o [Breaker] foi construído sem uma [state.Machine] (nil). Sem a
	// máquina durável não há para onde transitar o run ao dar trip; recusa fail-closed
	// na construção em vez de fabricar um breaker que não pode agir.
	ErrNilMachine = errors.New("breaker: state.Machine nil (sem máquina durável para transitar o run)")

	// ErrNotRunning — uma acção que exige o run em [state.Running] (escalar a humano /
	// abortar) foi pedida com o run noutro estado. O breaker só interrompe trabalho
	// VIVO; não há nada a interromper fora de running. Não é corrupção — é uma pré-
	// condição não satisfeita.
	ErrNotRunning = errors.New("breaker: run não está em running (nada a interromper)")

	// ErrVelocitySourceMissing — um limiar de velocity (MaxCostMicroUSDPerSecond ou
	// MaxTokensPerSecond) está LIGADO (>0) mas a [VelocitySource] não foi cablada
	// ([WithVelocitySource]). Sem a fonte, o escalar de velocity fica sempre a 0 e o
	// sinal NUNCA cruza — um breaker silenciosamente inerte para o sinal-cabeça de
	// "explosão de custo silenciosa". Recusa fail-closed na construção (à imagem de
	// [ErrNilMachine]) em vez de fabricar um breaker cego a um sinal que se julga activo.
	ErrVelocitySourceMissing = errors.New("breaker: limiar de velocity ligado sem VelocitySource cablada (WithVelocitySource)")

	// ErrProgressSourceMissing — o limiar de no-progress (MaxStaleIterations) está LIGADO
	// (>0) mas a [ProgressSource] não foi cablada ([WithProgressSource]). Sem a fonte, o
	// contador de iterações estéreis fica sempre a 0 e o sinal NUNCA cruza. Recusa
	// fail-closed na construção. Nota: o detector concreto de no-progress é AOS-081; até
	// existir, qualquer classe com MaxStaleIterations>0 tem de ligar explicitamente uma
	// porta de progresso (ou não pôr o limiar) — nunca um breaker morto e silencioso.
	ErrProgressSourceMissing = errors.New("breaker: limiar de no-progress ligado sem ProgressSource cablada (WithProgressSource)")

	// ErrProgressSourceInert — o limiar de no-progress (MaxStaleIterations) está LIGADO (>0)
	// e uma [ProgressSource] FOI cablada, mas ela declara-se INERTE via [EnabledSource]
	// (Enabled()==false — ex.: um detector de action-dedup de AOS-081 com Threshold<=0, cujo
	// MadeProgress é sempre true). A nil-check sozinha deixa passar esta fonte não-nil mas
	// cega ao sinal, produzindo o mesmo breaker "julga-se configurado mas está cego" que o
	// fail-closed da construção existe para impedir. Recusa fail-closed, fechando o buraco
	// simetricamente com [ErrProgressSourceMissing]. Remédio: alinhar o Threshold do detector
	// com o MaxStaleIterations da classe (ligar/desligar em conjunto).
	ErrProgressSourceInert = errors.New("breaker: limiar de no-progress ligado mas ProgressSource inerte (Enabled()==false; alinhe o Threshold do detector com MaxStaleIterations)")
)
