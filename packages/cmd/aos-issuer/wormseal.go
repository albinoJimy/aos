package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

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
	aceitarAncoras := fs.String("aceitar-ancoras", "", "ACEITAR digests NOVOS das ancoras de confianca (AOS-446 fase 1), separados por virgula: sem isto, ancoras trocadas desde a selagem anterior RECUSAM a selagem. O procedimento de rotacao de pinos produz DUAS mudancas (abrir e fechar a janela) e pode precisar dos dois digests")
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
		if jerr := json.Unmarshal(semBOM(bs), &anteriores); jerr != nil {
			return fmt.Errorf("aos-issuer: --anterior nao e JSON de checkpoints: %w", jerr)
		}
		if merr := exigirContinuidade(ctx, store, priv.Public().(ed25519.PublicKey), anteriores); merr != nil {
			return merr
		}
		// AOS-446 fase 1: e as ÂNCORAS DE CONFIANÇA do nó têm de ser as mesmas. Depois da
		// continuidade, e não antes: só faz sentido comparar com um passado que já se provou não
		// ter sido reescrito.
		// COM `--partition`, A VERIFICAÇÃO DAS ÂNCORAS NÃO CORRE — e diz-se (achado BAIXO 4 da 2.ª
		// ronda de revisão). Selar uma partição só é uma operação de recuperação; correr a
		// verificação sobre ela seria compará-la com uma base que a selagem não vai cobrir. O que
		// não se pode é ficar em silêncio: um `--partition` teclado por engano tornaria a
		// verificação num no-op sem ninguém dar por isso.
		if *partition != "" && *partition != audit.TrustAnchorsPartition {
			fmt.Fprintf(os.Stderr, "AVISO: com --partition=%q a verificacao das ANCORAS DE CONFIANCA (AOS-446) NAO CORREU. "+
				"Esta selagem nao prova nada sobre as ancoras do no; corra-a sem --partition para a fazer.\n", *partition)
		} else if merr := exigirAncorasIguais(ctx, store, anteriores, *aceitarAncoras, os.Stderr); merr != nil {
			return merr
		}
	} else {
		fmt.Fprintln(os.Stderr, "aviso: sem --anterior, a verificacao das ancoras de confianca (AOS-446) NAO corre — "+
			"nao ha selagem anterior com que comparar. O procedimento do operador passa sempre --anterior.")
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

// semBOM retira um BOM UTF-8 inicial. Ver a nota gémea no nó (`bom.go`) para o porquê: o
// PowerShell 5.1 escreve BOM em `Set-Content -Encoding utf8`, e o `encoding/json` recusa-o com
// «invalid character 'ï'». Descobri-o a correr o selador DUAS vezes seguidas — a segunda não leu
// o ficheiro que a primeira escreveu.
//
// Está duplicado de propósito e não factorizado: são módulos diferentes, e três linhas partilhadas
// não valem uma dependência nova entre o emissor e o nó. A duplicação está declarada nos dois
// sítios, que é o que a torna manutenível em vez de acidental.
func semBOM(raw []byte) []byte {
	return bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
}

// ------------------------------------------------------------------------------------------
// AS ÂNCORAS DE CONFIANÇA DO NÓ, VERIFICADAS FORA DO HOST (AOS-446 fase 1, ADR-033 §6.3 e §8)
//
// O DEFEITO. Quem tem root no host do nó troca `AOS_MANDATE_SIGNERS`, `AOS_ISSUER_PUBKEY`, a
// âncora da política ou a do próprio selador, reinicia, e o nó passa a servir sob outra
// autoridade. Nenhuma verificação DENTRO do processo protege contra quem reescreve o processo —
// o ADR-033 §2.1 já o dizia, e o §6.1 conta seis caminhos para lá.
//
// A METADE QUE SE PODE FECHAR. O nó sela, em cada arranque, as impressões das âncoras em uso
// (`packages/cmd/aos/ancoras_de_confianca.go`). Isso, sozinho, não vale muito: quem escreve no
// ficheiro do WORM também escreve lá o que quiser (o `EntryHash` é um SHA-256 SEM chave). O que
// fecha é ISTO correr AQUI: na máquina do operador, com uma chave que não está no host, sobre a
// cópia que o backup trouxe. A partir do momento em que uma selagem cobre o registo, a história
// até ali está congelada — a continuidade recusa selar por cima de uma reescrita — e a troca ou
// aparece, ou obriga a apagar o que já está ancorado, que é a outra recusa.
//
// O QUE NÃO SE FECHA, e é preciso dizê-lo com as mesmas letras:
//
//  1. **O TAIL AINDA NÃO ANCORADO.** Tudo o que está DEPOIS do último checkpoint é reescrevível
//     por inteiro por quem tem root — o `EntryHash` é um SHA-256 SEM chave, pelo que apagar os
//     registos do intervalo e re-encadear o que resta produz um WORM que verifica. A garantia
//     desta função vale para registos que JÁ ATRAVESSARAM uma selagem; para os outros, o que ela
//     faz é obrigar o atacante a reescrever em vez de acrescentar — e a reescrita, se for
//     descoberta, cai nas guardas da continuidade na selagem seguinte.
//  2. **A janela entre a troca e a selagem seguinte.** Se a troca e a reposição couberem as duas
//     dentro dela E o atacante reescrever o tail para as apagar, não fica rasto. Encolher a
//     janela é selar mais vezes; fechá-la exigiria uma testemunha independente — DEF-268.
//
// O que NÃO é limite (e era, até à revisão adversarial de 2026-09-27): acrescentar um registo com
// o retrato antigo por cima da troca. Ver [exigirAncorasIguais].
// ------------------------------------------------------------------------------------------

// ErrWormSealAncorasTrocadas — as âncoras de confiança do nó mudaram desde a selagem anterior, e
// o operador não o declarou. Não se sela.
var ErrWormSealAncorasTrocadas = errors.New("aos-issuer: as ANCORAS DE CONFIANCA do no MUDARAM desde a selagem anterior — se nao foi o operador a roda-las, o .env do host foi reescrito e o no esta a servir sob outra autoridade; selar aqui carimbaria a troca com a chave do selador")

// exigirAncorasIguais compara o retrato das âncoras que estava ancorado pela selagem anterior com
// o que está no WORM agora.
//
// `aceitar` é o digest NOVO que o operador declara esperar — o escape para uma rotação legítima.
// Exige-se o digest e não um `--sim`: escrever o valor obriga a olhar para ele, e um `--sim`
// aceitaria qualquer troca, incluindo a que aconteceu enquanto o operador rodava outra coisa.
func exigirAncorasIguais(ctx context.Context, store *audit.FileStore, anteriores []audit.Checkpoint, aceitar string, diag io.Writer) error {
	var ancorado uint64
	for _, cp := range anteriores {
		if cp.Partition == audit.TrustAnchorsPartition {
			ancorado = cp.AuditSeq
			break
		}
	}
	if ancorado == 0 {
		fmt.Fprintf(diag, "aviso: a selagem anterior nao cobria a particao %q — nao ha base com que comparar as ancoras de confianca (AOS-446). A partir desta selagem passa a haver.\n", audit.TrustAnchorsPartition)
		return nil
	}
	base, digestBase, seqBase, achouBase, err := audit.UltimasAncoras(ctx, store, ancorado)
	if err != nil {
		return fmt.Errorf("aos-issuer: ler as ancoras ancoradas em %d: %w", ancorado, err)
	}
	if !achouBase {
		fmt.Fprintf(diag, "aviso: a particao %q nao tem registos de ancoras legiveis ate %d — a verificacao do AOS-446 nao corre nesta selagem\n", audit.TrustAnchorsPartition, ancorado)
		return nil
	}
	// TODOS OS REGISTOS DO INTERVALO, E NAO SO O ULTIMO.
	//
	// O DEFEITO QUE ISTO FECHA (achado A1 da revisão adversarial de segurança, 2026-09-27,
	// PROVADO a correr). Esta função comparava o último registo ancorado com o último registo do
	// store — duas leituras — e concluía sobre o intervalo inteiro. Os registos DO MEIO nunca
	// eram lidos, e derrotá-la não exigia apagar nada:
	//
	//   1. root troca o pino e reinicia; o nó sela HONESTAMENTE um `trust_anchors.changed` com o
	//      retrato novo (seq N+1);
	//   2. root DEIXA a troca em vigor e faz append de um `trust_anchors.active` com os
	//      parâmetros ANTIGOS (seq N+2) — escrever no ficheiro do WORM é o que ele já podia fazer;
	//   3. a selagem seguinte lê o último (N+2, antigo), compara com o base (N, antigo), conclui
	//      «INALTERADAS» e SELA — carimbando a troca com a autoridade da chave do selador.
	//
	// Medido: exit 0 e a linha «INALTERADAS» sobre a sequência [honesto, do-atacante, honesto].
	// A varredura fecha-o porque o registo do passo 1 fica no caminho: a troca real deixou rasto,
	// e o rasto passa a ser LIDO. O que o atacante teria de fazer para o esconder é APAGAR o
	// registo N+1 — e aí caem as guardas que já existiam ([ErrWormSealRecuo] e
	// [ErrWormSealDivergencia]), porque o `audit_seq` é gapless dentro da partição.
	recs, err := store.Read(ctx, audit.TrustAnchorsPartition, ancorado+1, audit.TrustAnchorsMaxSeq)
	if err != nil {
		return fmt.Errorf("aos-issuer: ler as ancoras depois de %d: %w", ancorado, err)
	}
	declarados := digestsDeclarados(aceitar)
	usados := map[string]bool{}
	var varridos, ignorados int
	var naoReconhecidos []string
	var divergentes []string
	var ultimo audit.TrustAnchors
	digestAgora, seqAgora := digestBase, seqBase
	for _, rec := range recs {
		r, d, ok := audit.TrustAnchorsFromRecord(rec)
		if !ok {
			// UM REGISTO QUE NÃO SE RECONHECE CONTA, E DIZ-SE QUAL (achado BAIXO 5 da 2.ª ronda).
			// Saltá-lo em silêncio fazia o diagnóstico chegar a dizer «nenhum registo novo» sobre
			// uma partição onde alguém tinha escrito — que é precisamente o sítio onde um
			// registo estranho interessa.
			ignorados++
			naoReconhecidos = append(naoReconhecidos, fmt.Sprintf("seq %d (tipo %q)", rec.AuditSeq, rec.Resource.Type))
			continue
		}
		varridos++
		ultimo, digestAgora, seqAgora = r, d, rec.AuditSeq
		if d == digestBase {
			continue
		}
		if declarados[d] {
			usados[d] = true
			continue
		}
		divergentes = append(divergentes, fmt.Sprintf("seq %d (digest %s): %s",
			rec.AuditSeq, d, strings.Join(r.Diferencas(base), ", ")))
	}
	if ignorados > 0 {
		fmt.Fprintf(diag, "AVISO: %d registo(s) da particao %q sem retrato de ancoras legivel — %s. A verificacao do AOS-446 nao os cobre\n",
			ignorados, audit.TrustAnchorsPartition, strings.Join(naoReconhecidos, ", "))
	}
	if len(divergentes) > 0 {
		return fmt.Errorf("%w: entre o seq %d (ja ancorado) e o %d houve %d registo(s) de ancoras e %d com um retrato DIFERENTE do ancorado e NAO declarado — %s. "+
			"Se foi o operador a rodar: repita com --aceitar-ancoras a listar, SEPARADOS POR VIRGULA, o digest de cada retrato que reconhece",
			ErrWormSealAncorasTrocadas, seqBase, seqAgora, varridos, len(divergentes), strings.Join(divergentes, "; "))
	}
	switch {
	case varridos == 0 && ignorados > 0:
		// HAVIA registos — nenhum deles legível. Não se diz «nada novo»: diz-se o que há.
		fmt.Fprintf(diag, "ancoras de confianca: nenhum registo LEGIVEL desde a selagem anterior, mas a particao ganhou %d registo(s) que nao sao retratos de ancoras (ver o aviso acima); o ancorado em seq %d continua a ser a ultima afirmacao do no (digest %s)\n", ignorados, seqBase, digestBase)
	case varridos == 0:
		fmt.Fprintf(diag, "ancoras de confianca: nenhum registo de ancoras novo desde a selagem anterior (o no nao reiniciou); o ancorado em seq %d continua em vigor (digest %s)\n", seqBase, digestBase)
	case len(usados) > 0:
		fmt.Fprintf(diag, "ancoras de confianca: MUDARAM e o operador declarou-o — %d de %d digest(s) de --aceitar-ancoras usado(s) [%s], em %d registo(s) varrido(s) ate ao seq %d. No fim: %s\n",
			len(usados), len(declarados), strings.Join(ordenados(usados), ", "), varridos, seqAgora, strings.Join(ultimo.Diferencas(base), "; "))
	default:
		fmt.Fprintf(diag, "ancoras de confianca: INALTERADAS em TODOS os %d registo(s) entre o seq %d e o %d (digest %s)\n", varridos, seqBase, seqAgora, digestAgora)
	}
	// UM DIGEST DECLARADO QUE NÃO APARECEU não é erro, mas é informação: ou o operador o colou
	// errado, ou o registo que ele esperava não está no intervalo desta selagem.
	if sobram := naoUsados(declarados, usados); len(sobram) > 0 {
		fmt.Fprintf(diag, "nota: %d digest(s) de --aceitar-ancoras nao correspondem a nenhum registo do intervalo [%s]\n",
			len(sobram), strings.Join(sobram, ", "))
	}
	return nil
}

// digestsDeclarados lê a LISTA de `--aceitar-ancoras` (separada por vírgulas).
//
// PORQUE É UMA LISTA, E NÃO UM VALOR (achado B1 da 2.ª ronda de revisão de segurança). O flag
// guardava UM digest e comparava com `==` — e o procedimento de rotação de pinos da própria
// README produz DUAS mudanças do retrato: o passo que ABRE a janela (dois pinos) e o que a FECHA
// (volta a um). Se as duas caírem entre dois selos diários, NENHUMA forma de invocar o flag
// selava: nem vazio, nem o primeiro, nem o segundo, nem os dois de qualquer maneira — e a
// mensagem mandava declarar «o digest de CADA retrato que reconhece», que era impossível de
// cumprir. A saída que isso empurrava era largar o `--anterior`, e sem ele a verificação inteira
// nem corre. O flag passa a aceitar a lista, e compara-se por PERTENÇA.
func digestsDeclarados(bruto string) map[string]bool {
	out := map[string]bool{}
	for _, d := range strings.Split(bruto, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out[d] = true
		}
	}
	return out
}

func ordenados(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func naoUsados(declarados, usados map[string]bool) []string {
	var out []string
	for d := range declarados {
		if !usados[d] {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}
