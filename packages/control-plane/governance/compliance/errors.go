package compliance

import "errors"

// Sentinelas de erro do gerador de relatórios. Use errors.Is para ramificar.
var (
	// ErrTamperedAudit — o intervalo de que o relatório derivaria FALHOU a
	// verificação de integridade do audit ([audit.Verify]). Fail-closed: NÃO se gera
	// relatório sobre uma cadeia adulterada (AC4). Desembrulha para o erro concreto
	// de [audit.Verify] (ex.: [audit.ErrTampered]).
	ErrTamperedAudit = errors.New("compliance: audit adulterado no intervalo (relatorio nao gerado)")
	// ErrAnonymousAction — o relatório detectou pelo menos uma ACÇÃO ANÓNIMA (principal
	// não rastreável até um humano). Sinaliza fail-closed a violação do modelo de
	// responsabilização (AC1). [GenerateReport] devolve-o COM o relatório não-nil (as
	// acções anónimas estão em [ComplianceReport.Anomalies]) para forense.
	ErrAnonymousAction = errors.New("compliance: accao anonima detectada (principal sem raiz humana)")
	// ErrNilStore — [GenerateReport] sem um [audit.Store].
	ErrNilStore = errors.New("compliance: store de audit nil")
)
