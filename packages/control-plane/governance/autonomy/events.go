package autonomy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aos-ref/platform/audit"
)

// AOS-089 — as ALTERAÇÕES DE NÍVEL como EVENTOS AUDITÁVEIS selados na hash-chain
// WORM (AC5). Segue o molde do changelog policy.changed do PDP (AOS-088) e do
// retention.config.changed (AOS-092): traduz o facto num [audit.AuditRecord] cujo
// conteúdo canónico sela old→new/motivo/actor no EntryHash — logo TAMPER-EVIDENT,
// não apenas registado em memória.

// LevelChangedEventType é o rótulo estável do evento de alteração de nível no audit.
const LevelChangedEventType = "autonomy.level_changed"

// autonomyLevelCapability é a capability registada no evento (uniformidade com o
// vocabulário do audit: retention:config, policy:reload, ...).
const autonomyLevelCapability = "autonomy:set_level"

// DefaultAutonomyPartition é a partição de hash-chain por omissão dos eventos de
// autonomia. Isolá-los numa partição própria dá-lhes uma cadeia contígua (gapless)
// verificável independentemente das mediações de tool call e dos outros changelogs.
const DefaultAutonomyPartition = "autonomy"

// LevelChangeProofsParam é a chave de [audit.Obligation.Params] onde as [LevelChangeProof]
// de uma alteração vão seladas, como um array JSON.
//
// PORQUÊ NOS PARAMS e não noutro campo do registo: os Params entram no conteúdo canónico
// que produz o EntryHash (ordenados por chave, length-prefixed), pelo que a prova fica
// amarrada ao mesmo hash que o par, o nível, o motivo e o actor. Mudar uma assinatura
// depois de selada quebra a cadeia como mudar o nível quebraria.
//
// A chave só é ESCRITA quando há provas. Um evento sem provas (o provisionamento por
// configuração) produz exactamente os mesmos bytes canónicos que produzia antes de este
// campo existir — logo o mesmo EntryHash, e as cadeias já escritas continuam a verificar.
const LevelChangeProofsParam = "proofs"

// BuildLevelChangedRecord traduz uma [LevelChange] num [audit.AuditRecord]
// autonomy.level_changed, SEM o selar (AuditSeq/PrevHash/EntryHash são atribuídos
// por [audit.Store.Append]). Sela QUAL o par (agent/domain), a transição old→new,
// o MOTIVO e o ACTOR — todos ligados ao EntryHash pelo conteúdo canónico. Sem
// segredos: só identificadores e metadados de responsabilização.
func BuildLevelChangedRecord(ch LevelChange, partition string) audit.AuditRecord {
	if partition == "" {
		partition = DefaultAutonomyPartition
	}
	params := map[string]string{
		"agent":     ch.Agent,
		"domain":    ch.Domain,
		"old_level": ch.Old.String(),
		"new_level": ch.New.String(),
		"reason":    ch.Reason,
		"actor":     ch.Actor,
	}
	// PROVA (ver [LevelChangeProof]): as assinaturas do pedido entram no conteúdo selado.
	// json.Marshal de um slice de structs só com strings NÃO pode falhar; se falhasse, `b`
	// seria nil e o param ficaria vazio — o que a rehidratação lê como «sem prova» e o
	// validador do nó RECUSA. A direcção do erro é a segura.
	if len(ch.Proofs) > 0 {
		b, _ := json.Marshal(ch.Proofs)
		params[LevelChangeProofsParam] = string(b)
	}
	return audit.AuditRecord{
		Partition:  partition,
		Timestamp:  ch.At,
		Decision:   audit.DecisionAllow, // a alteração foi executada conforme a governação
		Principal:  audit.Principal{NHIID: ch.Actor},
		Capability: autonomyLevelCapability,
		Resource: audit.Resource{
			Type:  LevelChangedEventType,
			Value: ch.Agent + "/" + ch.Domain,
		},
		Obligations: []audit.Obligation{{
			Type:   LevelChangedEventType,
			Fields: []string{ch.Old.String(), ch.New.String()},
			Params: params,
		}},
	}
}

// AuditSink adapta um [audit.Store] à porta [Sink]: sela cada [LevelChange] como
// um registo autonomy.level_changed na hash-chain WORM. É a ligação do registo de
// níveis (GOV) ao audit tamper-evident (platform), sem acoplar o [LevelRegistry] ao
// subsistema de audit concreto.
type AuditSink struct {
	store     audit.Store
	partition string
}

// NewAuditSink constrói um [AuditSink] sobre o store dado. Uma partition vazia usa
// [DefaultAutonomyPartition].
func NewAuditSink(store audit.Store, partition string) *AuditSink {
	if partition == "" {
		partition = DefaultAutonomyPartition
	}
	return &AuditSink{store: store, partition: partition}
}

// SealLevelChange implementa [Sink]: sela a alteração na hash-chain WORM. Devolve o
// erro de [audit.Store.Append] (NÃO o engole), para que uma alteração de nível sem
// changelog selado seja detectável. Um sink/store nil é no-op (devolve nil).
func (s *AuditSink) SealLevelChange(ctx context.Context, ch LevelChange) error {
	if s == nil || s.store == nil {
		return nil
	}
	rec := BuildLevelChangedRecord(ch, s.partition)
	_, err := s.store.Append(ctx, rec)
	return err
}

// rehydrateMaxSeq é o tecto do intervalo de leitura na rehidratação. O store devolve o
// que existir até ao head; um valor alto evita ter de o consultar primeiro (o mesmo
// valor que o trilho de audit de cmd/aos usa — copiado, não importado).
const rehydrateMaxSeq = 1 << 40

// Pair identifica um par (agente, domínio) num [RehydrateReport].
type Pair struct {
	Agent  string
	Domain string
}

// RehydrateRejection é UM registo que a rehidratação SALTOU — que nunca chegou a ser
// aplicado — e que o arranque tem a obrigação de DECLARAR.
//
// PORQUE EXISTE (e porque não é um erro). Ver o bloco FAIL-CLOSED NO NÍVEL em
// [LevelRegistry.Rehydrate]: um registo que não se consegue confirmar não pode ser
// aplicado, mas também não pode ABORTAR o arranque, porque abortar entrega um modo de
// tijolo permanente ao MESMO adversário de que a verificação defende — quem tem escrita
// no ficheiro do WORM. Saltar fecha o vector de elevação sem abrir o de negação de
// serviço; o que o fecha por completo é esta estrutura chegar ao operador, em voz alta.
//
// É um FACTO OBSERVADO, não uma acusação: os campos abaixo são o que o registo RECLAMA
// (a palavra do store), e é precisamente por não se ter conseguido confirmar essa palavra
// que ele foi saltado.
type RehydrateRejection struct {
	// AuditSeq é o número de sequência do registo na partição — o que o operador usa para
	// o ir ver com as ferramentas de audit.
	AuditSeq uint64
	// Pair é o par (agente, domínio) que o registo pretendia governar. Pode vir vazio se
	// for esse o param em falta.
	Pair Pair
	// Level é o nível NOVO que o registo pedia. Fica em L0 quando o registo está malformado
	// ao ponto de o nível não parsear — nesse caso o literal está em [RehydrateRejection.LevelRaw]
	// e o motivo di-lo, para que um nível ilegível nunca seja apresentado como se fosse L0.
	Level Level
	// LevelRaw é o `new_level` LITERAL do registo, tal como estava nos Params.
	LevelRaw string
	// Actor é o actor que o registo reclama.
	Actor string
	// Motivo é a razão da recusa, tal como o validador (ou o parser) a deu.
	Motivo string
}

// RehydrateReport resume uma [LevelRegistry.Rehydrate]: quantos registos a partição
// tinha, quantos eram alterações de nível aplicadas, quais foram SALTADAS, e o ÚLTIMO
// actor por par — o dado de que o arranque precisa para decidir a precedência WORM vs
// ambiente (AOS-307).
type RehydrateReport struct {
	// Records é o total de registos lidos da partição (de qualquer tipo).
	Records int
	// Applied é o número de eventos autonomy.level_changed repostos no registo.
	Applied int
	// LastActorByPair é o actor da última alteração reposta, por par.
	LastActorByPair map[Pair]string
	// Rejeitados são os registos SALTADOS — nunca aplicados — pela ordem do stream. Uma
	// lista NÃO VAZIA é sempre uma anomalia a declarar: ou o WORM tem registos anteriores
	// ao mecanismo de provas (migração), ou alguém com escrita no ficheiro apendeu um
	// registo forjado. O chamador que a ignore reintroduz o silêncio que este campo existe
	// para fechar.
	Rejeitados []RehydrateRejection
}

// rehydrateOptions são os parâmetros opcionais de [LevelRegistry.Rehydrate].
type rehydrateOptions struct {
	validate func(LevelChange) error
}

// RehydrateOption configura uma [LevelRegistry.Rehydrate].
type RehydrateOption func(*rehydrateOptions)

// WithRehydrateValidator injecta o predicado que decide se uma alteração relida do WORM
// pode ser REPOSTA. É chamado para CADA alteração descodificada, ANTES de qualquer uma ser
// aplicada; um erro faz o registo ser SALTADO — nunca aplicado — e contabilizado em
// [RehydrateReport.Rejeitados] com o AuditSeq e o motivo (ver o bloco FAIL-CLOSED NO NÍVEL
// em [LevelRegistry.Rehydrate] para a razão de não abortar).
//
// PORQUE INJECTADO E NÃO IMPLEMENTADO AQUI. Verificar a prova de um registo exige as
// PUBKEYS dos operadores e o tuplo canónico do canal de controlo, que vivem na camada de
// composição (packages/integration + cmd/aos). Um pacote de governação a importá-los
// inverteria a fronteira de camadas — e o gate de lint recusa-o. A regra de confiança é do
// nó; o que é deste pacote é o ponto onde ela se aplica e a garantia de que se aplica
// FAIL-CLOSED, antes do efeito.
//
// SEM validador o comportamento é o de antes: reidrata tudo o que descodifique. É o modo
// dos testes de módulo — e é por isso que o nó TEM de passar o seu (ver
// `autonomyRehydrateValidator` em cmd/aos).
func WithRehydrateValidator(f func(LevelChange) error) RehydrateOption {
	return func(o *rehydrateOptions) {
		if f != nil {
			o.validate = f
		}
	}
}

// Rehydrate REPÕE o registo a partir do stream autonomy.level_changed da partição dada
// de um [audit.Store] (AOS-307; molde de Revocations.Rebuild). Lê a partição inteira,
// filtra as obrigações de tipo [LevelChangedEventType], faz o parse INVERSO de
// [BuildLevelChangedRecord] e aplica cada alteração pela ordem do stream, SEM voltar a
// selar (o selo já está no WORM). `At` de cada alteração é o Timestamp do registo.
//
// Destina-se ao ARRANQUE do nó — antes de aplicar o que o ambiente declara — para que
// um nível posto em vigor por POST /autonomy sobreviva a um reinício. Não é imposto que
// seja chamado antes do primeiro SetLevel, mas é para isso que existe.
//
// FAIL-CLOSED: um erro de leitura do store é devolvido; um registo com o tipo certo mas
// params em falta ou nível inválido é devolvido como erro que NOMEIA o AuditSeq — nunca é
// saltado em silêncio, porque um stream que não se consegue confirmar não pode ser
// servido como se fosse verdade. Nesse caso o registo fica como estava antes da chamada.
// Uma partição vazia ou inexistente (MemStore e FileStore devolvem slice vazio, sem erro)
// é o primeiro arranque legítimo: Applied=0, err=nil. Uma partition vazia usa
// [DefaultAutonomyPartition].
//
// AUTORIDADE DO QUE SE REIDRATA. O stream não se auto-autoriza: o EntryHash é um SHA-256
// SEM chave, pelo que quem escreve no ficheiro do store consegue apender um registo
// bem-formado que a re-verificação da cadeia aceita. Passe-se [WithRehydrateValidator]
// para confrontar cada alteração com uma raiz de confiança FORA do store (as pubkeys dos
// operadores) — sem ele, este método repõe tudo o que descodificar.
func (r *LevelRegistry) Rehydrate(ctx context.Context, store audit.Store, partition string, opts ...RehydrateOption) (RehydrateReport, error) {
	if store == nil {
		return RehydrateReport{}, errors.New("autonomy: rehidratar: store de audit nil")
	}
	if partition == "" {
		partition = DefaultAutonomyPartition
	}
	var cfg rehydrateOptions
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	recs, err := store.Read(ctx, partition, 1, rehydrateMaxSeq)
	if err != nil {
		return RehydrateReport{}, fmt.Errorf("autonomy: rehidratar: ler particao %q: %w", partition, err)
	}

	// FAIL-CLOSED NO NÍVEL, NÃO NO ARRANQUE.
	//
	// Um registo que não se consegue confirmar — malformado, ou de operador sem prova que
	// verifique — NUNCA é aplicado. Mas também NUNCA aborta a rehidratação. A primeira versão
	// abortava, com duas consequências medidas: (1) qualquer WORM escrito antes do mecanismo
	// de provas deixava o nó sem arrancar (o smoke do repositório falhou sobre o seu próprio
	// estado anterior); (2) quem tem escrita no ficheiro do WORM — a ameaça que a verificação
	// existe para conter — apendia UM registo malformado e o nó ficava permanentemente sem
	// arrancar. Trocava-se elevação de privilégio por negação de serviço, contra o MESMO
	// adversário, e a única saída era editar o ficheiro que não se deve poder editar.
	//
	// Saltar é seguro porque só pode deixar de ELEVAR um nível — nunca elevar: o par fica no
	// que o ambiente declarar, que é a postura anterior a AOS-307 e a mais restritiva das
	// duas. O que NÃO pode acontecer é o salto ser silencioso: cada registo saltado fica em
	// [RehydrateReport.Rejeitados] com o seq e o motivo, e o arranque tem a obrigação de o
	// declarar. Só a INDISPONIBILIDADE do substrato (o Read a falhar, acima) continua a ser
	// erro — isso não é um registo mau, é não haver com que arrancar.
	report := RehydrateReport{Records: len(recs), LastActorByPair: make(map[Pair]string)}
	changes := make([]LevelChange, 0, len(recs))
	for _, rec := range recs {
		for _, ob := range rec.Obligations {
			if ob.Type != LevelChangedEventType {
				continue
			}
			ch, err := parseLevelChangedObligation(ob, rec.Timestamp)
			if err != nil {
				report.Rejeitados = append(report.Rejeitados, rejeicaoDe(rec.AuditSeq, ob, err))
				continue
			}
			if cfg.validate != nil {
				if err := cfg.validate(ch); err != nil {
					report.Rejeitados = append(report.Rejeitados, rejeicaoDe(rec.AuditSeq, ob, err))
					continue
				}
			}
			changes = append(changes, ch)
		}
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	for _, ch := range changes {
		r.restore(ch)
		report.Applied++
		report.LastActorByPair[Pair{ch.Agent, ch.Domain}] = ch.Actor
	}
	return report, nil
}

// ParseLevel traduz "L0".."L5" (case-insensitive) num [Level]; (L0, false) para qualquer
// outra coisa. Exportado para que quem apresenta uma [RehydrateRejection] consiga distinguir
// «o registo pedia L0» de «o registo tinha um nível ilegível» — os dois deixam Level em L0.
func ParseLevel(s string) (Level, bool) { return parseLevel(s) }

// rejeicaoDe constrói a [RehydrateRejection] de um registo saltado a partir do que ele
// RECLAMA nos Params — não do que se conseguiu descodificar, precisamente porque pode não
// se ter conseguido. O nível só é traduzido se parsear; senão fica em L0 com o literal ao
// lado, para que um nível ilegível nunca apareça no banner disfarçado de L0.
func rejeicaoDe(seq uint64, ob audit.Obligation, motivo error) RehydrateRejection {
	p := ob.Params
	rej := RehydrateRejection{
		AuditSeq: seq,
		Pair:     Pair{Agent: p["agent"], Domain: p["domain"]},
		LevelRaw: p["new_level"],
		Actor:    p["actor"],
		Motivo:   motivo.Error(),
	}
	if lvl, ok := parseLevel(p["new_level"]); ok {
		rej.Level = lvl
	}
	return rej
}

// parseLevelChangedObligation é o inverso de [BuildLevelChangedRecord] para uma
// obrigação autonomy.level_changed. Fail-closed: param em falta ou nível fora de
// L0–L5 é erro.
func parseLevelChangedObligation(ob audit.Obligation, at time.Time) (LevelChange, error) {
	p := ob.Params
	for _, key := range []string{"agent", "domain", "old_level", "new_level", "reason", "actor"} {
		if p[key] == "" {
			return LevelChange{}, fmt.Errorf("param %q em falta", key)
		}
	}
	oldLvl, ok := parseLevel(p["old_level"])
	if !ok {
		return LevelChange{}, fmt.Errorf("old_level %q invalido", p["old_level"])
	}
	newLvl, ok := parseLevel(p["new_level"])
	if !ok {
		return LevelChange{}, fmt.Errorf("new_level %q invalido", p["new_level"])
	}
	// PROVA: opcional no formato (um evento de provisionamento não a tem) mas, quando
	// presente, tem de descodificar. JSON corrompido é ERRO e não «sem prova»: cair para
	// «sem prova» deixaria um adversário desactivar a verificação estragando o campo.
	var proofs []LevelChangeProof
	if raw := p[LevelChangeProofsParam]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &proofs); err != nil {
			return LevelChange{}, fmt.Errorf("param %q malformado: %v", LevelChangeProofsParam, err)
		}
	}
	return LevelChange{
		Agent:  p["agent"],
		Domain: p["domain"],
		Old:    oldLvl,
		New:    newLvl,
		Reason: p["reason"],
		Actor:  p["actor"],
		At:     at,
		Proofs: proofs,
	}, nil
}
