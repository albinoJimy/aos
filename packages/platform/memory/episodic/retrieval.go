package episodic

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/projection"
	"github.com/aos-ref/substrate/eventstore"
)

// Query é o critério de RECUPERAÇÃO de episódios (por objectivo e/ou tags). Um
// campo vazio não filtra nessa dimensão. A recuperação devolve sempre a PROJECÇÃO
// resumida — nunca a trajectória crua (Princípio 4).
type Query struct {
	// PrincipalID é a identidade VERIFICADA do principal (a NHI/AgentID) que
	// recupera. O recall é ESCOPADO por este principal: só devolve episódios
	// PRODUZIDOS por ele (env.AgentID == PrincipalID) — um recall de um principal
	// NUNCA devolve memória de outro. É fail-closed e OBRIGATÓRIO: vazio ⇒ a
	// recuperação é recusada com [ErrMissingPrincipal] (jamais devolve memória
	// alheia). Não é auto-declarado in-band pelo conteúdo do episódio — o chamador
	// passa aqui a identidade já verificada da request (o boundary de identidade
	// verifica-a a montante; a memória apenas a IMPÕE no escopo da leitura).
	PrincipalID string
	// Goal filtra por objectivo exacto (vazio = qualquer objectivo).
	Goal string
	// Tags são as etiquetas a casar. Ver MatchAll para a semântica.
	Tags []string
	// MatchAll: se true, o episódio tem de conter TODAS as Tags; se false, basta
	// conter pelo menos uma (any-of). Sem Tags, não filtra por tags.
	MatchAll bool
	// Limit limita o nº de resultados (0 = sem limite). Aplicado após o ranking.
	Limit int
}

// RecalledEpisode é um episódio recuperado: os campos de índice (em claro) e a
// PROJECÇÃO resumida decifrada. Recoverable=false indica um episódio cujo conteúdo
// é IRRECUPERÁVEL (chave apagada por crypto-shredding/TTL) — o índice permanece
// (e a hash-chain continua a verificar), mas a projecção perdeu-se.
type RecalledEpisode struct {
	EpisodeID string
	SubjectID string
	// AgentID é a NHI/principal que PRODUZIU o episódio. O recall é escopado por
	// este campo (== Query.PrincipalID); é exposto para tornar o escopo verificável.
	AgentID   string
	RunID     string
	TraceID   string
	Goal      string
	Tags      []string
	Outcome   string
	StepCount int
	AuditSeq  uint64
	// EmittedSpans é o nº de spans emitidos para o backend (registo COMPLETO). É
	// sempre >= aos turnos incluídos na projecção — o pai recebe o resumo, o backend
	// recebe a árvore completa (prova do Princípio 4 na recuperação).
	EmittedSpans int
	// Score é a pontuação de relevância (nº de tags casadas + bónus de objectivo).
	Score int
	// Recoverable indica se a projecção foi decifrada (a chave existe).
	Recoverable bool
	// Projection é a projecção resumida (só válida se Recoverable). NUNCA é a
	// trajectória crua — é o resumo higienizado e limitado em tokens de AOS-036.
	Projection projection.InjectedView
}

// Recall RECUPERA episódios por objectivo/tags e devolve-os por ordem de
// relevância DETERMINÍSTICA. Reconstrói o índice do Event Store (replay), filtra,
// pontua e ordena com tie-break estável. Para cada episódio recuperável, DECIFRA e
// devolve a PROJECÇÃO resumida (nunca a trajectória crua). Episódios cuja chave foi
// apagada (crypto-shredding/TTL) são devolvidos com Recoverable=false — visíveis no
// índice mas irrecuperáveis no conteúdo.
//
// Ranking determinístico: por Score DESC; empate por CreatedAt (audit_seq) ASC;
// empate remanescente por EpisodeID ASC. A mesma consulta sobre o mesmo log produz
// sempre a MESMA ordem.
//
// ESCOPO POR PRINCIPAL (fail-closed): a recuperação é SEMPRE restringida à
// identidade verificada do principal em [Query.PrincipalID] — só devolve episódios
// que ESSE principal produziu (env.AgentID == PrincipalID). Um recall de um
// principal NUNCA devolve memória de outro; um PrincipalID vazio RECUSA a
// recuperação com [ErrMissingPrincipal] (jamais devolve memória alheia por omissão).
func (s *TrajectoryStore) Recall(ctx context.Context, q Query) ([]RecalledEpisode, error) {
	// FAIL-CLOSED: sem principal verificado, não se recupera nada — o default é
	// negar, não abrir. É a primeira guarda, ANTES de sequer ler o log.
	if q.PrincipalID == "" {
		return nil, ErrMissingPrincipal
	}

	envs, err := s.readEnvelopes(ctx)
	if err != nil {
		return nil, err
	}

	type scored struct {
		env   episodeEnvelope
		score int
	}
	matched := make([]scored, 0, len(envs))
	for _, env := range envs {
		// ESCOPO POR PRINCIPAL: descarta silenciosamente qualquer episódio que NÃO
		// pertença ao principal que recupera (env.AgentID != PrincipalID). Um recuo
		// cross-principal resolve para vazio — nunca expõe memória alheia.
		if env.AgentID != q.PrincipalID {
			continue
		}
		if q.Goal != "" && env.Goal != q.Goal {
			continue
		}
		score, ok := scoreTags(env.Tags, q.Tags, q.MatchAll)
		if !ok {
			continue
		}
		if q.Goal != "" && env.Goal == q.Goal {
			score++ // bónus de objectivo exacto
		}
		matched = append(matched, scored{env: env, score: score})
	}

	// Ranking determinístico e estável.
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].score != matched[j].score {
			return matched[i].score > matched[j].score
		}
		if matched[i].env.AuditSeq != matched[j].env.AuditSeq {
			return matched[i].env.AuditSeq < matched[j].env.AuditSeq
		}
		return matched[i].env.EpisodeID < matched[j].env.EpisodeID
	})

	out := make([]RecalledEpisode, 0, len(matched))
	for _, m := range matched {
		re := RecalledEpisode{
			EpisodeID:    m.env.EpisodeID,
			SubjectID:    m.env.SubjectID,
			AgentID:      m.env.AgentID,
			RunID:        m.env.RunID,
			TraceID:      m.env.TraceID,
			Goal:         m.env.Goal,
			Tags:         append([]string(nil), m.env.Tags...),
			Outcome:      m.env.Outcome,
			StepCount:    m.env.StepCount,
			AuditSeq:     m.env.AuditSeq,
			EmittedSpans: m.env.EmittedSpans,
			Score:        m.score,
		}
		iv, derr := s.decrypt(m.env)
		switch {
		case derr == nil:
			re.Recoverable = true
			re.Projection = iv
		case errors.Is(derr, ErrEpisodeShredded):
			re.Recoverable = false
		default:
			return nil, derr
		}
		out = append(out, re)
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}

// Project devolve a PROJECÇÃO resumida de UM episódio pelo seu id. É o caminho
// crisp de recuperação/prova: um episódio cuja chave foi apagada devolve
// [ErrEpisodeShredded] (irrecuperável), NUNCA a trajectória crua nem um plaintext.
// Um id inexistente devolve [ErrEpisodeNotFound].
//
// ESCOPO POR PRINCIPAL (fail-closed): tal como [TrajectoryStore.Recall], a prova
// por-id é SEMPRE restringida à identidade verificada do principal em principalID —
// só devolve o episódio se ESSE principal o produziu (env.AgentID == principalID). Um
// principalID vazio RECUSA a prova com [ErrMissingPrincipal] (nunca devolve conteúdo
// por omissão do escopo). Um episódio existente que pertença a OUTRO principal é
// indistinguível de inexistente — devolve [ErrEpisodeNotFound], NÃO um erro distinto:
// isto fecha o oráculo de existência cross-principal (um principal não confirma sequer
// que o id de outro existe). Fecha o caminho de leitura de conteúdo por-id que, sem
// escopo, devolvia a projecção decifrada de QUALQUER episódio a quem soubesse o id.
func (s *TrajectoryStore) Project(ctx context.Context, principalID, episodeID string) (projection.InjectedView, error) {
	// FAIL-CLOSED: sem principal verificado, não se prova nada — o default é negar.
	if principalID == "" {
		return projection.InjectedView{}, ErrMissingPrincipal
	}
	envs, err := s.readEnvelopes(ctx)
	if err != nil {
		return projection.InjectedView{}, err
	}
	for _, env := range envs {
		if env.EpisodeID != episodeID {
			continue
		}
		// ESCOPO POR PRINCIPAL: um episódio de OUTRO principal é indistinguível de
		// inexistente (não-oráculo de existência) — fail-closed, nunca expõe conteúdo
		// nem confirma a existência de um id alheio.
		if env.AgentID != principalID {
			return projection.InjectedView{}, ErrEpisodeNotFound
		}
		return s.decrypt(env)
	}
	return projection.InjectedView{}, ErrEpisodeNotFound
}

// decrypt decifra o envelope de um episódio para a sua projecção. Fail-closed: se a
// chave do titular foi apagada (crypto-shredding) OU a decifragem falha, devolve
// [ErrEpisodeShredded] — o episódio é irrecuperável (o índice e a cadeia mantêm-se).
func (s *TrajectoryStore) decrypt(env episodeEnvelope) (projection.InjectedView, error) {
	kek, ok := s.keys.Key(env.SubjectID)
	if !ok {
		return projection.InjectedView{}, ErrEpisodeShredded
	}
	plaintext, err := open(kek, env.Sealed)
	if err != nil {
		// Chave errada/rotacionada/blob adulterado: também irrecuperável.
		return projection.InjectedView{}, ErrEpisodeShredded
	}
	var iv projection.InjectedView
	if err := json.Unmarshal(plaintext, &iv); err != nil {
		return projection.InjectedView{}, err
	}
	return iv, nil
}

// scoreTags devolve a pontuação de casamento de tags e se o episódio passa o
// filtro. Sem tags de consulta, passa com score 0. MatchAll exige todas; caso
// contrário basta uma (any-of), e o score é o nº de tags casadas.
func scoreTags(have, want []string, matchAll bool) (int, bool) {
	if len(want) == 0 {
		return 0, true
	}
	set := make(map[string]struct{}, len(have))
	for _, t := range have {
		set[t] = struct{}{}
	}
	n := 0
	for _, w := range want {
		if _, ok := set[w]; ok {
			n++
		}
	}
	if matchAll {
		return n, n == len(want)
	}
	return n, n > 0
}

// ResumeFrom compõe um episódio recuperado com o Event Store para obter o CURSOR
// de retoma resume-from-step (AOS-015/016): o episódio dá o run_id (recuperado por
// objectivo/tags) e o Event Store dá os checkpoints. É a materialização de "um
// episódio recuperado é suficiente para replay resume-from-step EM CONJUNTO com o
// Event Store" — a memória episódica COMPLEMENTA a indexação, não substitui o ES.
//
// Requer que o EventLog subjacente seja também um durable.EventStore (o mesmo
// método-set) — *eventstore.Store é. Reutiliza o durable.Resumer de AOS-015 (não o
// reimplementa): relê o stream do run e devolve o próximo passo não confirmado.
func (s *TrajectoryStore) ResumeFrom(ctx context.Context, ep RecalledEpisode) (durable.ResumePoint, error) {
	if ep.RunID == "" {
		return durable.ResumePoint{}, ErrMissingRunID
	}
	store, ok := s.es.(durable.EventStore)
	if !ok {
		return durable.ResumePoint{}, ErrResumeUnsupported
	}
	resumer, err := durable.NewResumer(store)
	if err != nil {
		return durable.ResumePoint{}, err
	}
	return resumer.Resume(ctx, ep.RunID)
}

// TTLPolicy mapeia cada classe de retenção à sua duração de vida. Uma duração <= 0
// significa SEM EXPIRAÇÃO (ex.: permanent). É a base do TTL POR CLASSE (ADR-011).
type TTLPolicy map[domain.TTLClass]time.Duration

// DefaultTTLPolicy é a política de TTL por classe default. Coerente com a taxonomia
// de AOS-035: efémero curto, permanente sem expiração. Os valores são ilustrativos
// (produção calibra por conformidade); o mecanismo é o que importa.
func DefaultTTLPolicy() TTLPolicy {
	return TTLPolicy{
		domain.TTLEphemeral: 1 * time.Hour,
		domain.TTLShort:     24 * time.Hour,
		domain.TTLStandard:  30 * 24 * time.Hour,
		domain.TTLLongLived: 365 * 24 * time.Hour,
		domain.TTLPermanent: 0, // sem expiração
	}
}

// SweptEpisode identifica um episódio expirado por TTL e crypto-shredded.
type SweptEpisode struct {
	EpisodeID string
	SubjectID string
	TTLClass  string
}

// Sweep aplica o TTL POR CLASSE via crypto-shredding, respeitando que a chave é POR
// TITULAR (uma KEK por subject embrulha TODOS os seus episódios). Apagar a KEK
// expira, de uma vez, todos os episódios do titular — logo o TTL só pode agir à
// GRANULARIDADE DO TITULAR, não do episódio isolado.
//
// Invariante de segurança (não-destruição de não-expirados): a KEK de um titular só
// é apagada quando TODOS os seus episódios já expiraram. Se algum episódio do titular
// ainda estiver dentro do TTL — ou for de uma classe SEM expiração (ex.: permanent) —
// a KEK é RETIDA e NADA desse titular é varrido. Isto evita a perda silenciosa de
// episódios não-expirados (incluindo permanentes) que partilham o titular com um
// expirado. Quando a KEK É apagada, TODOS os episódios do titular são reportados em
// swept (todos ficam irrecuperáveis), não apenas o que disparou a expiração.
//
// Apagar a KEK torna os episódios IRRECUPERÁVEIS sem apagar o registo do log nem
// partir a hash-chain (ADR-011: "episódios expiram por política sem quebrar a cadeia
// de hash"). Determinístico (relógio via now; ordem de varrimento = ordem de escrita).
func (s *TrajectoryStore) Sweep(ctx context.Context, now time.Time) ([]SweptEpisode, error) {
	envs, err := s.readEnvelopes(ctx)
	if err != nil {
		return nil, err
	}

	// Agrupa por titular preservando a ordem de escrita (determinismo). Para cada
	// titular contamos episódios totais vs. expirados: só shreddamos quando 100%
	// expiraram (um único não-expirado retém a KEK).
	type subjectState struct {
		total    int
		expired  int
		episodes []episodeEnvelope
	}
	states := make(map[string]*subjectState)
	var order []string
	for _, env := range envs {
		st := states[env.SubjectID]
		if st == nil {
			st = &subjectState{}
			states[env.SubjectID] = st
			order = append(order, env.SubjectID)
		}
		st.total++
		st.episodes = append(st.episodes, env)

		ttl, ok := s.ttlPolicy[domain.TTLClass(env.TTLClass)]
		if !ok || ttl <= 0 {
			continue // classe sem expiração (ex.: permanent) ou desconhecida: nunca expira
		}
		created, perr := time.Parse(time.RFC3339Nano, env.CreatedAt)
		if perr != nil {
			return nil, perr
		}
		if now.Before(created.Add(ttl)) {
			continue // ainda dentro do TTL: conta como não-expirado
		}
		st.expired++
	}

	var swept []SweptEpisode
	for _, subject := range order {
		st := states[subject]
		if st.total == 0 || st.expired != st.total {
			continue // algum episódio não-expirado (ou permanente): RETÉM a KEK
		}
		s.keys.DeleteKey(subject)
		for _, env := range st.episodes {
			swept = append(swept, SweptEpisode{
				EpisodeID: env.EpisodeID,
				SubjectID: env.SubjectID,
				TTLClass:  env.TTLClass,
			})
		}
	}
	return swept, nil
}

// errorsIsStreamNotFound isola a dependência do sentinela do Event Store (um stream
// ainda sem eventos não é erro — é índice vazio).
func errorsIsStreamNotFound(err error) bool {
	return errors.Is(err, eventstore.ErrStreamNotFound)
}
