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

	// ErrSourceBehindBackup — o log da FONTE está atrás do cursor da cadeia do destino: há um
	// stream cujo head é menor do que o que o backup declara já ter exportado (ou que a fonte nem
	// enumera). Só há duas causas, e nenhuma autoriza continuar: a cadeia é de OUTRO log (outro nó,
	// outro Event Store com a mesma chave e a mesma região), ou este log foi REBOBINADO (restaurado
	// para um ponto anterior). Nos dois casos continuar saltaria esse stream em silêncio e, quando o
	// head voltasse a passar o cursor, coseria duas histórias diferentes numa cadeia que verifica.
	//
	// O CICLO recusa (não produz segmento nem registo) e não decide o que fazer a seguir: continuar
	// o backup de um log rebobinado é decisão de operação (tipicamente, um destino novo para a cadeia
	// nova). Fail-closed, AGENTS.md §7.8.
	//
	// NÃO é verificado no ARRANQUE, de propósito: um PITR, um WAL truncado na recuperação ou o DR
	// real (restauro de uma cópia de volume) deixam o log atrás do cursor, e recusar a construção
	// impediria o NÓ de subir — no preciso momento em que se está a recuperá-lo. O primeiro ciclo
	// recusa sem escrever, e o agendador pára com a causa nomeada.
	ErrSourceBehindBackup = errors.New("backup: o log da fonte esta ATRAS do cursor da cadeia no destino (cadeia de outro log, ou log rebobinado) — o exportador recusa continuar")

	// ErrCycleRecordInvalid — a escrita do registo de ciclo colidiu ([ErrImmutable]) e o registo que
	// lá está NÃO verifica com a chave deste exportador (assinatura, índice, região ou elo). Não é
	// uma re-tentativa nossa nem outro exportador com a mesma chave: é adulteração ou lixo no
	// destino, e escala como tal.
	ErrCycleRecordInvalid = errors.New("backup: o registo de ciclo que ocupa a referencia no destino NAO verifica com a chave deste exportador (adulteracao ou lixo, nao bifurcacao)")

	// ErrDestinationNotConditional — o destino ACEITOU uma segunda escrita na mesma referência. A
	// retoma assenta em write-once condicional: sem ele dois exportadores deixam de colidir em
	// [ErrChainOwned] e bifurcam a cadeia em silêncio, e um ciclo reescreve o registo de outro. Em
	// S3 o Object Lock sozinho NÃO basta (um PUT sobre uma chave existente cria uma versão nova e
	// devolve sucesso): a escrita tem de ser condicional (`If-None-Match: *` ou equivalente).
	ErrDestinationNotConditional = errors.New("backup: o destino aceitou uma SEGUNDA escrita na mesma referencia — a porta ImmutableStore exige Put condicional (ErrImmutable numa ref existente; em S3, If-None-Match: *), e o exportador recusa arrancar sobre um destino que nao o cumpre")

	// ErrInvalidKey — chave ed25519 de dimensão inválida.
	ErrInvalidKey = errors.New("backup: chave ed25519 invalida")

	// ErrConfig — configuração do exportador/restaurador inválida.
	ErrConfig = errors.New("backup: configuracao invalida")

	// ErrRestoreVerify — a verificação do manifesto falhou durante um restauro; o
	// restauro é ABORTADO antes de escrever qualquer evento (fail-closed).
	ErrRestoreVerify = errors.New("backup: verificacao do backup falhou; restauro abortado")
)
