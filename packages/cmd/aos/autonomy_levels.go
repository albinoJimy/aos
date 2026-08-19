package main

// NÍVEIS DE AUTONOMIA (AOS-087) — o que torna o `escalate` ALCANÇÁVEL.
//
// O veredicto `escalate` do Reference Monitor — a entrada do bridge de aprovação humana
// (AOS-021) — NÃO vem de uma regra Cedar: Cedar só exprime permit/deny. Vem do ORÁCULO DE
// AUTONOMIA, que o PDP consulta depois de uma decisão de base `permit`: compõe o nível
// corrente do par (agente, domínio) com a classe de risco da acção e, se o modo de
// oversight exigir um humano (suggest/confirm/batch), REBAIXA o permit para escalate.
//
// Sem oráculo ligado, `applyAutonomy` é NO-OP e o escalate NUNCA dispara — o bridge de
// aprovação fica construído mas inalcançável. Este ficheiro liga-o por configuração.
//
// PORQUE OPT-IN E NÃO LIGADO POR OMISSÃO: o registo é FAIL-CLOSED — um par sem nível
// registado resolve L0, cujo oversight é `suggest` para TODAS as classes. Ligar o oráculo
// com um registo vazio faria CADA tool call exigir aprovação humana individual, o que
// pararia qualquer deployment existente. Quais agentes correm em que domínios e a que
// nível é uma decisão POR-DEPLOYMENT, não um default que se possa inventar. Sem a
// variável, o comportamento é exactamente o de antes.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/aos-ref/control-plane/governance/autonomy"
	audit "github.com/aos-ref/platform/audit"
)

// ErrBadAutonomyLevels — AOS_AUTONOMY_LEVELS está definido mas é inválido. Fail-closed:
// quem declara níveis obtém-nos bem-formados ou o nó recusa arrancar. Um par mal escrito
// que fosse ignorado em silêncio deixaria o agente a correr sob L0 (tudo escalado) ou —
// pior — sob um nível diferente do pretendido.
var ErrBadAutonomyLevels = errors.New("aos: AOS_AUTONOMY_LEVELS mal configurado — formato `agente:dominio=Ln` separado por virgulas (ex.: `agt-1:http=L4,agt-1:fs=L5`), com Ln em L0..L5. L0=sugestao (tudo escala), L1=confirma cada accao, L2=lote (danger confirma), L3=tiering SA-ROC, L4=corre e so danger confirma, L5=corre e danger fica em amostragem post-hoc")

// autonomyLevelSpec é uma entrada declarada: o par (agente, domínio) e o seu nível.
type autonomyLevelSpec struct {
	agent  string
	domain string
	level  autonomy.Level
}

// parseAutonomyLevels lê AOS_AUTONOMY_LEVELS. Vazio ⇒ (nil, nil): oráculo NÃO ligado,
// comportamento inalterado.
func parseAutonomyLevels() ([]autonomyLevelSpec, error) {
	return parseAutonomyLevelsFrom(os.Getenv("AOS_AUTONOMY_LEVELS"))
}

// parseAutonomyLevelsFrom analisa a MESMA gramatica a partir de uma string qualquer.
//
// Extraido para que a SIMULACAO use exactamente o parser do arranque. Se usasse outro, aceitaria
// configuracoes que o no recusaria — e daria luz verde a uma tabela que nunca chegaria a entrar
// em vigor.
func parseAutonomyLevelsFrom(entrada string) ([]autonomyLevelSpec, error) {
	raw := strings.TrimSpace(entrada)
	if raw == "" {
		return nil, nil
	}
	var out []autonomyLevelSpec
	for _, entrada := range strings.Split(raw, ",") {
		entrada = strings.TrimSpace(entrada)
		if entrada == "" {
			continue
		}
		par, nivel, ok := strings.Cut(entrada, "=")
		if !ok {
			return nil, fmt.Errorf("%w: entrada %q sem `=`", ErrBadAutonomyLevels, entrada)
		}
		// ALVO: instância `agt-1:fs`, ou CLASSE `class:agent-worker:fs`.
		//
		// A classe é a unidade ESTÁVEL: os agent_id são cunhados por run, pelo que registar
		// instâncias é registar coisas que ainda não existem. O prefixo é explícito e não
		// adivinhado — não se tenta inferir "isto parece uma classe" do formato do nome, porque
		// uma inferência errada aqui muda silenciosamente o alcance de uma regra de autonomia.
		alvo := strings.TrimSpace(par)
		ehClasse := strings.HasPrefix(alvo, autonomy.ClassPrefix)
		if ehClasse {
			alvo = strings.TrimPrefix(alvo, autonomy.ClassPrefix)
		}
		agente, dominio, ok := strings.Cut(alvo, ":")
		if !ok {
			return nil, fmt.Errorf("%w: entrada %q sem `agente:dominio` (ou `class:<classe>:<dominio>`)", ErrBadAutonomyLevels, entrada)
		}
		agente, dominio = strings.TrimSpace(agente), strings.TrimSpace(dominio)
		if agente == "" || dominio == "" {
			return nil, fmt.Errorf("%w: entrada %q com agente ou dominio vazio", ErrBadAutonomyLevels, entrada)
		}
		// Um id de INSTÂNCIA não pode invadir o namespace das classes: senão `class:x:fs` seria
		// ambíguo entre "a classe x" e "o agente literalmente chamado class:x".
		if !ehClasse && strings.HasPrefix(agente, strings.TrimSuffix(autonomy.ClassPrefix, ":")) && strings.Contains(agente, ":") {
			return nil, fmt.Errorf("%w: entrada %q — um agente nao pode usar o prefixo reservado %q", ErrBadAutonomyLevels, entrada, autonomy.ClassPrefix)
		}
		if ehClasse {
			agente = autonomy.ClassPrefix + agente
		}
		lvl, err := parseAutonomyLevel(strings.TrimSpace(nivel))
		if err != nil {
			return nil, fmt.Errorf("%w: entrada %q: %v", ErrBadAutonomyLevels, entrada, err)
		}
		out = append(out, autonomyLevelSpec{agent: agente, domain: dominio, level: lvl})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: lista vazia", ErrBadAutonomyLevels)
	}
	return out, nil
}

// parseAutonomyLevel traduz "L0".."L5" (case-insensitive) no [autonomy.Level].
func parseAutonomyLevel(s string) (autonomy.Level, error) {
	switch strings.ToUpper(s) {
	case "L0":
		return autonomy.L0, nil
	case "L1":
		return autonomy.L1, nil
	case "L2":
		return autonomy.L2, nil
	case "L3":
		return autonomy.L3, nil
	case "L4":
		return autonomy.L4, nil
	case "L5":
		return autonomy.L5, nil
	default:
		return autonomy.L0, fmt.Errorf("nivel %q desconhecido (use L0..L5)", s)
	}
}

// autonomyProvisionReason / autonomyProvisionActor são o MOTIVO e a ATRIBUIÇÃO com que o
// provisionamento sela cada nível. [autonomy.LevelRegistry.SetLevel] RECUSA um motivo vazio
// ([autonomy.ErrMissingReason]) ou um actor vazio ([autonomy.ErrMissingActor]) — e bem: uma
// promoção anónima ou sem justificação na hash-chain não responsabiliza ninguém. A atribuição
// aqui é honesta: quem decidiu o nível foi quem escreveu AOS_AUTONOMY_LEVELS no deployment,
// não o nó; o nó não inventa um humano que não participou.
const (
	autonomyProvisionReason = "provisionamento por AOS_AUTONOMY_LEVELS"
	autonomyProvisionActor  = "config:node"
)

// ErrAutonomySinkUnbound — pediu-se a selagem de uma alteração de nível antes de o WORM do nó
// estar ligado ao sink. FAIL-CLOSED e nunca silencioso: [autonomy.LevelRegistry.SetLevel]
// DEVOLVE o erro do sink a quem chama, pelo que uma alteração de nível sem selo aborta o
// arranque em vez de entrar em vigor sem rasto. É a guarda estrutural da ligação tardia
// descrita em [autonomyWORMSink] — sem ela, uma inversão futura na ordem do boot voltaria a
// dar níveis de autonomia sem registo, que é exactamente o defeito que AOS-248 fecha.
var ErrAutonomySinkUnbound = errors.New("aos: alteracao de nivel de autonomia SEM WORM ligado — o sink de audit ainda nao foi ligado ao store composto do no; o provisionamento tem de correr DEPOIS de o WORM existir (Bootstrap), nunca na fronteira de config")

// ErrAutonomyProvisioning — o provisionamento dos níveis declarados falhou: ou o registo
// recusou a entrada, ou o WORM recusou SELAR a alteração. Fail-closed no molde do resto do
// boot: um nó que não consegue registar — nem selar — a autonomia com que vai correr não
// arranca com uma autonomia por adivinhar.
var ErrAutonomyProvisioning = errors.New("aos: provisionamento dos niveis de autonomia (AOS_AUTONOMY_LEVELS) falhou — o nivel nao ficou registado NEM selado na hash-chain WORM; o no recusa arrancar com uma autonomia que nao consegue auditar")

// autonomyWORMSink é o [autonomy.Sink] do nó, com LIGAÇÃO TARDIA ao WORM composto.
//
// PORQUÊ TARDIA E NÃO DIRECTA. O registo de níveis nasce na fronteira de CONFIG
// ([loadPolicyBundleFromEnv]) porque tem de ser passado a [pdp.Open] como opção; o WORM só
// existe DEPOIS, no composition-root ([Bootstrap]), que é quem o abre e detém o seu ciclo de
// vida. Abrir aqui um segundo store — ou selar num audit in-memory de conveniência — daria
// selos que ninguém verifica: o selo tem de cair na MESMA hash-chain que o resto do nó, a que
// [audit.VerifyStore] re-encadeia no arranque e que o operador consegue auditar.
//
// A janela entre construir e ligar é fechada por ERRO, não por silêncio: enquanto não houver
// store, [autonomyWORMSink.SealLevelChange] devolve [ErrAutonomySinkUnbound] e o SetLevel que
// a provocasse aborta o arranque.
//
// Seguro para concorrência porque o registo é consultado (LevelFor) pelo caminho de decisão
// enquanto o boot ainda pode estar a ligar o sink.
type autonomyWORMSink struct {
	mu    sync.RWMutex
	inner autonomy.Sink
}

// bind liga o sink ao WORM COMPOSTO do nó. A partição fica na de omissão
// ([autonomy.DefaultAutonomyPartition]): uma cadeia contígua só de eventos de autonomia é
// verificável independentemente das mediações de tool call e dos outros changelogs.
func (s *autonomyWORMSink) bind(worm audit.Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner = autonomy.NewAuditSink(worm, "")
}

// SealLevelChange implementa [autonomy.Sink]. Sem store ligado NEGA — ver [ErrAutonomySinkUnbound].
func (s *autonomyWORMSink) SealLevelChange(ctx context.Context, ch autonomy.LevelChange) error {
	s.mu.RLock()
	inner := s.inner
	s.mu.RUnlock()
	if inner == nil {
		return ErrAutonomySinkUnbound
	}
	return inner.SealLevelChange(ctx, ch)
}

// autonomyWiring é a cablagem do oráculo em DUAS FASES, imposta pela ordem do arranque:
//
//  1. [buildAutonomyOracle], na fronteira de config, constrói o registo JÁ com o sink ligado
//     por [autonomy.WithSink] e entrega o [autonomy.Oracle] ao [pdp.Open];
//  2. [autonomyWiring.provision], no composition-root, liga o sink ao WORM composto e só
//     ENTÃO aplica os níveis declarados — pelo que cada SetLevel de provisionamento nasce
//     selado, com motivo e actor, na hash-chain do nó.
//
// Entre as duas fases o registo responde L0 a TODO o par (o fail-closed de
// [autonomy.LevelRegistry.LevelFor]): o nó ainda não serve nada, e se servisse a postura
// seria a mais restritiva, não a mais permissiva.
type autonomyWiring struct {
	registry *autonomy.LevelRegistry
	sink     *autonomyWORMSink
	specs    []autonomyLevelSpec
	// sealedPairs são os pares agente:domínio cujo SetLevel foi de facto APLICADO E SELADO
	// por [autonomyWiring.provision] — o ESTADO, distinto de `specs`, que é só o DECLARADO.
	// Existe porque o banner de postura afirma "cada um SELADO na hash-chain WORM" e essa
	// afirmação tem de derivar de algo que registe que o provisionamento CORREU, não da
	// ordem em que o boot por acaso chama as coisas (achado F-A6 da auditoria da W0: hoje o
	// binário está certo porque [Bootstrap] provisiona antes de imprimir, mas uma reordenação
	// reintroduziria um banner falso com o teste verde).
	//
	// É um CONJUNTO e não um contador porque a cardinalidade anunciada é de PARES: duas
	// entradas para o mesmo par (`agt-1:http=L4,agt-1:http=L5`) selam dois eventos mas
	// governam UM par — anunciar "2 par(es)" seria falso.
	//
	// Sem mutex de propósito: escrito só em [provision] e lido só pelo banner, ambos no
	// composition-root, na mesma goroutine e antes de o nó servir seja o que for. O que é
	// consultado concorrentemente (LevelFor) vive no registo, que tem o seu.
	sealedPairs map[string]struct{}
	// piso e o nivel dos pares SEM registo (AOS_AUTONOMY_DEFAULT). Guardado para o banner e para
	// o GET /autonomy poderem DECLARA-LO: um par ausente da lista nao e "sem politica".
	piso autonomy.Level
}

// buildAutonomyOracle constrói o registo de níveis a partir das entradas declaradas, com o
// [autonomy.Sink] JÁ ligado (fase 1). nil ⇒ oráculo não ligado (o PDP não aplica oversight de
// autonomia e nada escala). Não regista nível nenhum aqui: registar antes de haver WORM daria
// alterações de nível sem selo — ver [autonomyWiring].
func buildAutonomyOracle(specs []autonomyLevelSpec, piso autonomy.Level) *autonomyWiring {
	if len(specs) == 0 {
		return nil
	}
	sink := &autonomyWORMSink{}
	return &autonomyWiring{
		registry:    autonomy.NewLevelRegistry(autonomy.WithSink(sink), autonomy.WithDefaultLevel(piso)),
		piso:        piso,
		sink:        sink,
		specs:       specs,
		sealedPairs: make(map[string]struct{}),
	}
}

// oracle devolve o [autonomy.Oracle] a ligar ao PDP ([pdp.WithAutonomyOracle]). Receptor nil ⇒
// interface nil, para que o chamador não ligue um oráculo-fantasma.
func (w *autonomyWiring) oracle() autonomy.Oracle {
	if w == nil {
		return nil
	}
	return w.registry
}

// provision é a fase 2: liga o sink ao WORM composto e aplica os níveis declarados. Receptor
// nil ⇒ no-op (oráculo não ligado, comportamento inalterado). worm nil ⇒ recusa: sem store não
// há selo, e um nível não-selado é o defeito que este caminho existe para impedir.
func (w *autonomyWiring) provision(ctx context.Context, worm audit.Store) error {
	if w == nil {
		return nil
	}
	if worm == nil {
		return ErrAutonomySinkUnbound
	}
	w.sink.bind(worm)
	for _, s := range w.specs {
		// SetLevel aplica E sela. A selagem falhada NÃO é engolida pelo registo (devolve o erro
		// junto com a change já aplicada), pelo que a propagamos: o arranque aborta.
		if _, err := w.registry.SetLevel(ctx, s.agent, s.domain, s.level,
			autonomyProvisionReason, autonomyProvisionActor); err != nil {
			return fmt.Errorf("%w: %s:%s=%s: %v", ErrAutonomyProvisioning, s.agent, s.domain, s.level, err)
		}
		// Só DEPOIS do SetLevel bem sucedido: o conjunto regista o que ficou mesmo selado, e é
		// a partir dele — não de `specs` — que o banner afirma "cada um SELADO".
		w.sealedPairs[s.agent+":"+s.domain] = struct{}{}
	}
	return nil
}

// ErrBadAutonomyDefault — AOS_AUTONOMY_DEFAULT presente mas fora de L0..L5.
var ErrBadAutonomyDefault = errors.New("aos: AOS_AUTONOMY_DEFAULT invalida (esperado L0..L5, ou ausente para o piso L0)")

// parseAutonomyDefault interpreta o PISO dos pares sem nível registado.
//
// VAZIO ⇒ L0, exactamente como antes: um nó que não a defina não muda de comportamento. Um valor
// FORA do vocabulário ABORTA o arranque em vez de cair no valor-zero — que é L0 e passaria por
// "aceite" enquanto ignorava em silêncio o que o operador escreveu. Um typo que produz a postura
// mais restritiva é o pior tipo de typo: ninguém o vai procurar, porque nada parece errado.
func parseAutonomyDefault() (autonomy.Level, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_AUTONOMY_DEFAULT"))
	if raw == "" {
		return autonomy.L0, nil
	}
	lvl, err := parseAutonomyLevel(raw)
	if err != nil {
		return autonomy.L0, fmt.Errorf("%w: %q", ErrBadAutonomyDefault, raw)
	}
	return lvl, nil
}
