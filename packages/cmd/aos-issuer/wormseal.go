package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	audit "github.com/aos-ref/platform/audit"
)

// ------------------------------------------------------------------------------------------
// worm-seal — PRODUZ as âncoras assinadas que o nó já sabe consumir.
//
// O DEFEITO QUE FECHA. O nó tem a verificação ancorada completa e fail-closed
// ([audit.VerifyFromCheckpointAtHead], `AOS_WORM_TRUST_ANCHOR` + `AOS_WORM_CHECKPOINT_FILE` +
// `AOS_WORM_EXPECTED_HEAD`, as três obrigatórias em conjunto). Só que NADA no repositório produzia
// um checkpoint: ninguém instanciava o [audit.Signer] fora de testes, e o `IngestPipeline.Seal`
// não estava ligado a comando nenhum.
//
// O inventário de conceitos dizia que a verificação ancorada «hoje ancoraria 1 em 108». Ancorava
// ZERO: as três variáveis nunca podiam ser preenchidas. Uma porta fail-closed que ninguém consegue
// abrir não é uma porta trancada — é uma parede, e o banner descrevia-a como opção.
//
// PORQUE ISTO VIVE NO `aos-issuer` E NÃO NO NÓ. Selar exige a chave PRIVADA, e o contrato
// (AOS-156, e a própria mensagem de [ErrBadWormTrustAnchor]) diz que ela «vive e assina FORA do
// nó». O nó CONSOME a âncora; não a produz. Este comando corre onde as chaves do operador já
// vivem.
//
// CONTRA QUE WORM SE SELA. [audit.Signer.Seal] precisa do STORE para ler o hash da entrada — e o
// store vive no servidor, onde a chave não pode estar. A saída é selar contra a cópia que o
// backup já traz off-host (caminho provado: ver o ensaio de restauro), e não contra o servidor
// vivo.
//
// E SELA-SE O QUE SE VERIFICOU. Antes de assinar, este comando RE-ENCADEIA o store. Sem isso, o
// operador assinaria o que um servidor comprometido lhe entregasse, e a âncora certificaria a
// falsificação com a autoridade da chave dele.
//
// NOTA HONESTA sobre essa guarda: o `OpenFileStore` JÁ valida a cadeia ao abrir, pelo que nenhum
// vector de ficheiro chega ao [ErrWormSealChainBroken] — é uma segunda rede, e fica declarada
// como tal em vez de contada como a defesa principal.
//
// A defesa que FECHA o vector principal é outra, e não é o re-encadeamento: uma cadeia TRUNCADA
// re-encadeia como íntegra, e uma cadeia REESCRITA de raiz também. É a CONTINUIDADE face à selagem
// anterior ([exigirContinuidade]) que impede fabricar uma âncora válida sobre um WORM que perdeu
// registos — ou sobre um que os tenha todos, mas OUTROS. A guarda comparou primeiro só o
// COMPRIMENTO, e essa metade sozinha deixava passar a reescrita; hoje corre a MESMA verificação
// ancorada que o nó corre no arranque.
//
// O LIMITE QUE FICA, e é inerente a qualquer esquema de checkpoint sem testemunha independente:
// a âncora prova que a cadeia NÃO MUDOU DESDE A SELAGEM. Não prova que era honesta ANTES dela. O
// que se fecha é a truncatura do tail e a reescrita desde a génese POSTERIORES ao selo.
// ------------------------------------------------------------------------------------------

// ErrWormSealChainBroken — a cadeia de uma partição não re-encadeia. Não se assina.
var ErrWormSealChainBroken = errors.New("aos-issuer: a hash-chain do WORM NAO re-encadeia — selar aqui daria a uma cadeia partida a autoridade da chave do selador")

// sealSaida é o que o comando emite: as âncoras assinadas e, SEPARADO, o piso de frescura.
//
// Os dois NÃO vão no mesmo ficheiro de propósito. O piso existe para recusar um checkpoint
// LEGÍTIMO mas ANTERIOR, reapresentado para mascarar a truncatura do que veio depois. Se viajasse
// no mesmo ficheiro, quem trocasse o ficheiro trocava os dois — e o piso deixaria de morder
// exactamente no ataque que existe para fechar. Tem de ser persistido INDEPENDENTEMENTE.
type sealSaida struct {
	Checkpoints []audit.Checkpoint `json:"checkpoints"`
	// Heads é o material para `AOS_WORM_EXPECTED_HEAD`, por partição. Imprime-se à parte para o
	// operador o guardar noutro sítio — ver o comentário acima.
	Heads map[string]uint64 `json:"-"`
}

func runWormSeal(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("worm-seal", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	wormPath := fs.String("worm", "", "caminho do ficheiro WORM a selar (tipicamente a copia trazida pelo backup, NAO o servidor vivo)")
	keyFile := fs.String("key-file", "", "ficheiro com a seed ed25519 (32 bytes em hex) do SELADOR de checkpoints")
	partition := fs.String("partition", "", "selar SO esta particao (vazio ⇒ TODAS as que o store conhece)")
	anterior := fs.String("anterior", "", "ficheiro de checkpoints da selagem ANTERIOR — recusa selar se alguma particao ja ancorada tiver DESAPARECIDO ou RECUADO")
	heads := fs.Bool("heads", false, "imprimir os pisos de frescura (AOS_WORM_EXPECTED_HEAD) em vez dos checkpoints")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *wormPath == "" {
		return errors.New("aos-issuer: --worm obrigatorio (ficheiro WORM a selar)")
	}
	if *keyFile == "" {
		return errors.New("aos-issuer: --key-file obrigatorio (seed do selador; a chave NUNCA e ecoada)")
	}

	priv, err := loadApproverKey(*keyFile) // MESMO carregador dos outros comandos: a seed nunca é ecoada.
	if err != nil {
		return err
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		return fmt.Errorf("aos-issuer: selador invalido: %w", err)
	}

	store, err := audit.OpenFileStore(*wormPath)
	if err != nil {
		return fmt.Errorf("aos-issuer: abrir WORM %q: %w", *wormPath, err)
	}
	defer store.Close()

	ctx := context.Background()
	// RE-ENCADEAMENTO ANTES DE ASSINAR. `VerifyStore` percorre TODAS as partições; uma cadeia
	// partida em qualquer uma delas impede a selagem de todas, e é deliberado: um store que não
	// re-encadeia não é um store de onde se tire uma âncora de confiança.
	if _, verr := audit.VerifyStore(ctx, store); verr != nil {
		return fmt.Errorf("%w: %v", ErrWormSealChainBroken, verr)
	}

	// CONTINUIDADE face a selagem anterior. O re-encadeamento acima NAO chega: uma cadeia truncada
	// re-encadeia como integra, e uma REESCRITA tambem â sem esta guarda selar-se-ia uma ancora
	// valida sobre qualquer das duas.
	if *anterior != "" {
		bs, rerr := os.ReadFile(*anterior)
		if rerr != nil {
			return fmt.Errorf("aos-issuer: ler --anterior %q: %w", *anterior, rerr)
		}
		var anteriores []audit.Checkpoint
		if jerr := json.Unmarshal(bs, &anteriores); jerr != nil {
			return fmt.Errorf("aos-issuer: --anterior nao e JSON de checkpoints: %w", jerr)
		}
		if merr := exigirContinuidade(ctx, store, priv.Public().(ed25519.PublicKey), anteriores); merr != nil {
			return merr
		}
	}

	alvos := store.Partitions()
	if *partition != "" {
		alvos = []string{*partition}
	}
	sort.Strings(alvos)

	saida := sealSaida{Heads: map[string]uint64{}}
	for _, p := range alvos {
		head, herr := store.Head(ctx, p)
		if herr != nil {
			return fmt.Errorf("aos-issuer: head de %q: %w", p, herr)
		}
		if head == 0 {
			continue // partição vazia: não há nada para ancorar.
		}
		cp, serr := signer.Seal(ctx, store, p, head)
		if serr != nil {
			return fmt.Errorf("aos-issuer: selar %q@%d: %w", p, head, serr)
		}
		saida.Checkpoints = append(saida.Checkpoints, cp)
		saida.Heads[p] = head
	}
	if len(saida.Checkpoints) == 0 {
		return errors.New("aos-issuer: nenhuma particao com registos para selar")
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if *heads {
		// Os pisos, para o operador os persistir NOUTRO sítio que não o ficheiro de checkpoints.
		return enc.Encode(saida.Heads)
	}
	return enc.Encode(saida.Checkpoints)
}

// ErrWormSealRecuo — o store a selar perdeu registos face à selagem anterior. Não se sela.
var ErrWormSealRecuo = errors.New("aos-issuer: o WORM RECUOU face a selagem anterior (particao desaparecida ou head abaixo do ja ancorado) — selar aqui daria uma ancora VALIDA a uma cadeia TRUNCADA")

// exigirContinuidade recusa selar um store cuja historia ancorada nao seja a mesma â por ter
// PERDIDO registos, ou por os ter SUBSTITUIDO.
//
// PORQUE EXISTE, e foi um teste a falhar pela razão certa que o revelou: o re-encadeamento
// (`VerifyStore`) NÃO apanha a truncatura. Uma cadeia truncada re-encadeia como íntegra — é o
// vector que a verificação ancorada existe para fechar, e está declarado no banner do nó.
//
// Consequência: sem esta guarda, alimentar o selador com uma cópia truncada produzia uma âncora
// PERFEITAMENTE VÁLIDA para uma cadeia TRUNCADA. O nó aceitá-la-ia para sempre, e a âncora
// passaria a certificar a truncatura com a autoridade da chave do selador — o oposto exacto do
// que existe para fazer.
//
// É o mesmo princípio do `AOS_WORM_EXPECTED_HEAD`, aplicado do lado da PRODUÇÃO: o piso recusa
// uma âncora antiga reapresentada; isto recusa fabricar uma âncora nova sobre menos do que já
// havia. As duas metades do mesmo argumento, e nenhuma serve sozinha.
func exigirContinuidade(ctx context.Context, store *audit.FileStore, pub ed25519.PublicKey, anteriores []audit.Checkpoint) error {
	for _, cp := range anteriores {
		head, err := store.Head(ctx, cp.Partition)
		if err != nil || head == 0 {
			return fmt.Errorf("%w: particao %q ja ancorada em %d DESAPARECEU (err=%v)",
				ErrWormSealRecuo, cp.Partition, cp.AuditSeq, err)
		}
		if head < cp.AuditSeq {
			return fmt.Errorf("%w: particao %q ancorada em %d e o store so tem %d",
				ErrWormSealRecuo, cp.Partition, cp.AuditSeq, head)
		}
		// CONTINUIDADE, e não só comprimento. Esta é a metade que faltava: a verificação acima
		// recusa uma partição que ENCOLHEU, mas uma história REESCRITA com o mesmo número de
		// registos — ou mais — passava por ela sem tocar em nada. O selador produziria então uma
		// âncora PERFEITAMENTE VÁLIDA sobre a falsificação, e o nó aceitá-la-ia para sempre: a
		// reescrita desde a génese, assinada pela chave do selador, que é o pior desfecho possível.
		//
		// A verificação é a MESMA que o nó corre no arranque ([audit.VerifyFromCheckpoint]): a
		// âncora anterior tem de continuar a bater com o registo real daquele audit_seq, e a
		// cadeia daí até ao head tem de re-encadear. Não se reimplementa a regra.
		if err := audit.VerifyFromCheckpoint(ctx, store, pub, cp, head); err != nil {
			return fmt.Errorf("%w: a particao %q DIVERGIU do que ja estava ancorado em %d (%v) — "+
				"o comprimento nao encolheu, mas a historia nao e a mesma",
				ErrWormSealDivergencia, cp.Partition, cp.AuditSeq, err)
		}
	}
	return nil
}

// ErrWormSealDivergencia — a história ancorada mudou. Não se sela por cima.
var ErrWormSealDivergencia = errors.New("aos-issuer: o WORM DIVERGIU do que ja estava ancorado — selar aqui daria a autoridade da chave do selador a uma historia REESCRITA, que e o vector que a verificacao ancorada existe para fechar")
