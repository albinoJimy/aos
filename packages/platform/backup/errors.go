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

	// ErrResumeUnverifiable — o estado de RETOMA encontrado no destino não verifica: a assinatura
	// do checkpoint não valida contra a chave pública do signer, o elo não recomputa a partir do
	// conteúdo canónico, a região não é a deste destino, ou o head assinado não é o do elo que o
	// acompanha. Fail-closed no ARRANQUE: o exportador NÃO é construído.
	//
	// A alternativa — continuar na mesma — seria pior do que qualquer outro erro deste módulo: um
	// registo de ciclo com StreamHeads acima do real faria o exportador SALTAR os eventos
	// intermédios, e o backup ficaria com um buraco que nada acusaria até ao dia do restauro.
	ErrResumeUnverifiable = errors.New("backup: estado de retoma do destino nao verifica; o exportador RECUSA arrancar em vez de continuar uma cadeia que nao prova ser sua")

	// ErrChainOwned — o registo do ciclo JÁ EXISTE no destino: outro exportador selou este ciclo
	// nesta cadeia. Não é adulteração e não é um destino avariado — são DOIS ESCRITORES sobre o
	// mesmo backup, e a referência indexada do registo de ciclo existe precisamente para que a
	// segunda escrita seja recusada em vez de bifurcar a cadeia em silêncio.
	//
	// A distinção é a mesma que o AOS-284 fez na hash-chain da auditoria: uma bifurcação tem uma
	// causa (dois donos) e uma correcção (um destino por exportador) que nada têm a ver com as de
	// uma adulteração, e colapsá-las mandaria o operador procurar um atacante onde há uma
	// configuração repetida.
	ErrChainOwned = errors.New("backup: o ciclo ja foi selado neste destino por OUTRO exportador (dois escritores sobre a mesma cadeia — bifurcacao, nao adulteracao)")

	// ErrSegmentRefCollision — a referência (endereçada por conteúdo) do segmento já existe no
	// destino com conteúdo DIFERENTE: uma colisão de prefixo do content-hash. Praticamente
	// impossível com 8 bytes — mas a re-tentativa idempotente de um ciclo interrompido depende de
	// distinguir "o mesmo objecto já lá está" de "outro objecto está no meu caminho", e essa
	// distinção não pode assentar num prefixo. Confirma-se sempre pelo hash INTEIRO.
	ErrSegmentRefCollision = errors.New("backup: a referencia do segmento ja existe com CONTEUDO DIFERENTE (colisao de prefixo do content-hash)")

	// ErrInvalidKey — chave ed25519 de dimensão inválida.
	ErrInvalidKey = errors.New("backup: chave ed25519 invalida")

	// ErrConfig — configuração do exportador/restaurador inválida.
	ErrConfig = errors.New("backup: configuracao invalida")

	// ErrRestoreVerify — a verificação do manifesto falhou durante um restauro; o
	// restauro é ABORTADO antes de escrever qualquer evento (fail-closed).
	ErrRestoreVerify = errors.New("backup: verificacao do backup falhou; restauro abortado")
)
