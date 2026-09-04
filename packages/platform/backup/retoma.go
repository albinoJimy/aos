package backup

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
)

// retoma.go — o exportador passa a poder CONTINUAR um backup começado por outro processo.
//
// # O defeito que isto fecha
//
// [NewExporter] começava SEMPRE do génesis: manifesto vazio, cursor vazio. Contra um
// destino em memória isso não se nota — o destino morre com o processo. Contra um destino
// que SOBREVIVE ao processo, o primeiro ciclo de CADA arranque tenta escrever
// `<região>/seg-00000001`, que já lá está, e o write-once devolve [ErrImmutable]. E devolve
// PARA SEMPRE, porque o índice nunca avança.
//
// Medido em reinicio_test.go antes de existir remédio. É a razão pela qual não se ligou um
// backend durável no AOS-101: teria produzido segmentos que deixavam de ser escritos ao
// segundo arranque — uma promessa de backup pior do que a ausência dela.
//
// # Porque a retoma NÃO precisa de decifrar nada
//
// A tentação era reconstruir o manifesto a partir dos segmentos. Não serve: o `EntryHash`
// de cada entrada cobre a contagem de eventos e os `StreamHeads`, que só existem DENTRO do
// payload cifrado — logo reconstruir exigiria a KEK, e a KEK de um processo novo não
// decifra segmentos de um processo antigo a menos que o cofre também sobreviva. Ficaríamos
// com uma retoma que depende de duas durabilidades em vez de uma.
//
// O manifesto, esse, é seguro de guardar no MESMO destino, e o seu próprio doc di-lo: «não
// contém plaintext nem segredos — só hashes, refs e cobertura de seq por stream». Cada
// ciclo escreve o seu, sob uma ref determinística, e o write-once não estorva porque cada
// ciclo tem índice próprio. A retoma passa a ser: encontrar o último, lê-lo, verificá-lo.
//
// # Porque se PROCURA em vez de se listar
//
// O [ImmutableStore] não tem `List` — só `Put`/`Get`/`Delete`/`Region`. Acrescentar `List`
// mudaria um contrato que já tem implementações, e por uma razão que não é essencial: as
// refs são determinísticas e DENSAS (`seg-00000001`, `-2`, … sem buracos), pelo que o
// último índice encontra-se por procura. Duplica-se para achar o primeiro ausente e depois
// bissecta-se — O(log n) chamadas a `Get` em vez de O(n).

// refDoManifesto devolve a ref determinística do manifesto do ciclo dado.
//
// Mesmo esquema dos segmentos (`%08d`), de propósito: quem inspecciona o destino à mão vê
// os dois lado a lado e na mesma ordem.
func refDoManifesto(regiao string, ciclo uint64) string {
	return fmt.Sprintf("%s/manifest-%08d", regiao, ciclo)
}

// estadoPersistido é o que se escreve por ciclo: o manifesto e o checkpoint assinado.
//
// Os dois juntos e não só o manifesto: o checkpoint é a raiz de confiança, e uma retoma
// que carregasse o manifesto sem ele teria de confiar no que leu do destino sem ter com que
// o confrontar.
type estadoPersistido struct {
	Manifesto  Manifest   `json:"manifest"`
	Checkpoint Checkpoint `json:"checkpoint"`
}

// persistirEstado escreve o manifesto e o checkpoint do ciclo no destino.
//
// Uma falha aqui NÃO invalida o segmento já escrito — o segmento é durável e o manifesto em
// memória está correcto. O que se perde é a capacidade de RETOMAR a partir deste ciclo; o
// arranque seguinte retomaria do último ciclo cujo estado ficou escrito e voltaria a
// colidir no primeiro segmento que já lá está. Por isso o erro SOBE: um exportador que não
// consegue persistir o seu estado está a construir um backup que não se consegue continuar,
// e é melhor sabê-lo agora do que no próximo arranque.
func (e *Exporter) persistirEstado(ciclo uint64) error {
	est := estadoPersistido{Manifesto: e.manifest, Checkpoint: e.lastCheckpoint}
	blob, err := json.Marshal(est)
	if err != nil {
		return fmt.Errorf("backup: serializar estado do ciclo %d: %w", ciclo, err)
	}
	ref := refDoManifesto(e.manifest.Region, ciclo)
	// Sem object-lock: o estado é metadado de operação, não é o backup. Prendê-lo pela
	// política de retenção dos DADOS impediria a limpeza de um destino cujos segmentos já
	// expiraram, sem proteger nada que os segmentos não protejam melhor.
	if err := e.dst.Put(ref, blob, e.now()); err != nil {
		return fmt.Errorf("backup: persistir estado do ciclo %d em %q: %w", ciclo, ref, err)
	}
	return nil
}

// RetomarDoDestino reconstrói o estado do exportador a partir do último ciclo escrito no
// destino, para que um processo novo CONTINUE o backup em vez de recomeçar.
//
// Chama-se UMA vez, antes do primeiro [Exporter.Export]. Num destino vazio não faz nada e
// devolve nil — um exportador acabado de criar sobre um destino novo já está no estado
// certo, e exigir uma chamada diferente para esse caso obrigaria o chamador a saber qual
// dos dois mundos tem antes de perguntar.
//
// VERIFICA o que carrega: a assinatura do checkpoint contra a chave pública dada, e a
// cadeia do manifesto contra esse checkpoint. Um destino adulterado é RECUSADO em vez de
// continuado — retomar de um manifesto forjado escreveria segmentos legítimos por cima de
// uma história falsa, e o resultado verificaria como íntegro.
func (e *Exporter) RetomarDoDestino(ctx context.Context, pub ed25519.PublicKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.started || len(e.manifest.Segments) > 0 {
		return errors.New("backup: RetomarDoDestino tem de ser chamado ANTES do primeiro Export")
	}
	ultimo, err := e.ultimoCicloNoDestino()
	if err != nil {
		return err
	}
	if ultimo == 0 {
		return nil // destino vazio: nada a retomar, e não é erro
	}

	blob, err := e.dst.Get(refDoManifesto(e.manifest.Region, ultimo))
	if err != nil {
		return fmt.Errorf("backup: ler o estado do ciclo %d: %w", ultimo, err)
	}
	var est estadoPersistido
	if err := json.Unmarshal(blob, &est); err != nil {
		return fmt.Errorf("backup: estado do ciclo %d ilegível: %w", ultimo, err)
	}
	if est.Manifesto.Region != e.manifest.Region {
		return fmt.Errorf("%w: o destino tem estado da região %q e este exportador é da região %q",
			ErrSovereigntyViolation, est.Manifesto.Region, e.manifest.Region)
	}
	if uint64(len(est.Manifesto.Segments)) != ultimo {
		return fmt.Errorf("backup: o estado do ciclo %d declara %d segmentos — o manifesto não corresponde ao ciclo",
			ultimo, len(est.Manifesto.Segments))
	}

	// VERIFICAÇÃO antes de adoptar. A ordem importa: primeiro a assinatura (a raiz de
	// confiança), depois a cadeia CONTRA ela.
	if err := VerifyCheckpoint(pub, est.Checkpoint); err != nil {
		return fmt.Errorf("backup: checkpoint do destino não verifica: %w", err)
	}
	verificador, err := NewRestorer(e.dst, e.vault, pub)
	if err != nil {
		return fmt.Errorf("backup: construir o verificador da retoma: %w", err)
	}
	if err := verificador.VerifyManifest(est.Manifesto, est.Checkpoint, ultimo); err != nil {
		return fmt.Errorf("backup: o manifesto do destino não verifica — retomar daqui escreveria por cima de uma história que não se prova: %w", err)
	}

	// ADOPTA. O cursor incremental vem dos StreamHeads cumulativos do ÚLTIMO segmento,
	// que é exactamente o que o Export usa para saber o que já foi exportado.
	e.manifest = est.Manifesto
	e.lastCheckpoint = est.Checkpoint
	e.lastExported = cloneHeads(est.Manifesto.Segments[ultimo-1].StreamHeads)
	e.started = true
	return nil
}

// ultimoCicloNoDestino devolve o maior ciclo com estado escrito, ou 0 se o destino está
// vazio. Duplica para achar um ausente e depois bissecta — O(log n) `Get`.
func (e *Exporter) ultimoCicloNoDestino() (uint64, error) {
	existe := func(c uint64) (bool, error) {
		_, err := e.dst.Get(refDoManifesto(e.manifest.Region, c))
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, ErrNotFound):
			return false, nil
		default:
			return false, fmt.Errorf("backup: procurar o ciclo %d no destino: %w", c, err)
		}
	}
	ha, err := existe(1)
	if err != nil || !ha {
		return 0, err
	}
	// baixo EXISTE sempre; alto NÃO existe sempre. A resposta é `baixo` quando se tocam.
	baixo := uint64(1)
	alto := uint64(2)
	for {
		ha, err := existe(alto)
		if err != nil {
			return 0, err
		}
		if !ha {
			break
		}
		baixo = alto
		alto *= 2
	}
	for alto-baixo > 1 {
		meio := baixo + (alto-baixo)/2
		ha, err := existe(meio)
		if err != nil {
			return 0, err
		}
		if ha {
			baixo = meio
		} else {
			alto = meio
		}
	}
	return baixo, nil
}
