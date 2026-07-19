package backup

import "errors"

// Sentinelas do módulo de backup. Comparáveis com errors.Is.
var (
	// ErrSovereigntyViolation — o destino do backup cruza (ou não prova respeitar)
	// a fronteira regional de soberania do board (ADR-011): região do destino
	// ausente, desconhecida ou diferente da região do Event Store. Fail-closed:
	// backups e cópias NUNCA cruzam a fronteira.
	ErrSovereigntyViolation = errors.New("backup: destino cruza a fronteira regional de soberania (fail-closed)")

	// ErrImmutable — tentativa de SOBRESCREVER uma referência já existente no
	// ImmutableStore (object-lock/WORM write-once). O segmento imutável nunca é
	// mutado; a segunda escrita à mesma ref é recusada.
	ErrImmutable = errors.New("backup: referencia ja existe (object-lock write-once)")

	// ErrObjectLocked — tentativa de APAGAR um objecto ainda dentro do período de
	// object-lock (retenção) ou sob legal hold. O WORM impede a remoção antecipada.
	ErrObjectLocked = errors.New("backup: objecto sob object-lock/legal-hold; remocao recusada")

	// ErrNotFound — referência inexistente no ImmutableStore.
	ErrNotFound = errors.New("backup: referencia inexistente")

	// ErrSegmentTampered — um segmento do backup foi adulterado: o SHA-256 do blob
	// em repouso não corresponde ao ContentHash selado no manifesto. Detecção de
	// tamper (ADR-010) ANTES sequer de tentar decifrar.
	ErrSegmentTampered = errors.New("backup: segmento adulterado (content-hash divergente)")

	// ErrChainBroken — a hash-chain do manifesto não fecha: um EntryHash recomputado
	// diverge do selado, ou o head não corresponde ao checkpoint assinado. O backup
	// não é confiável.
	ErrChainBroken = errors.New("backup: hash-chain do manifesto quebrada")

	// ErrCheckpointSignature — a assinatura do checkpoint não valida contra a chave
	// pública (âncora de confiança). Um checkpoint forjado é rejeitado.
	ErrCheckpointSignature = errors.New("backup: assinatura de checkpoint invalida")

	// ErrCheckpointStale — o checkpoint apresentado é anterior ao head conhecido do
	// backup (rollback de checkpoint). Fail-closed contra reapresentação de um
	// manifesto truncado (molde de audit.VerifyFromCheckpointAtHead).
	ErrCheckpointStale = errors.New("backup: checkpoint anterior ao head conhecido (rollback)")

	// ErrInvalidKey — chave ed25519 de dimensão inválida.
	ErrInvalidKey = errors.New("backup: chave ed25519 invalida")

	// ErrConfig — configuração do exportador/restaurador inválida.
	ErrConfig = errors.New("backup: configuracao invalida")

	// ErrRestoreVerify — a verificação do manifesto falhou durante um restauro; o
	// restauro é ABORTADO antes de escrever qualquer evento (fail-closed).
	ErrRestoreVerify = errors.New("backup: verificacao do backup falhou; restauro abortado")
)
