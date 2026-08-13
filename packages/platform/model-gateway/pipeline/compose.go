package pipeline

import "context"

// StageFunc adapta uma função à interface [Stage] com um nome dado. Útil para
// os tickets de extensão (AOS-058..062) e para testes construírem estágios sem
// declarar um tipo. O nome vai para o rasto de decisões e para o [StageError].
type StageFunc struct {
	StageName string
	Fn        func(ctx context.Context, ex *Exchange) error
}

// Name implementa [Stage].
func (s StageFunc) Name() string { return s.StageName }

// Process implementa [Stage].
func (s StageFunc) Process(ctx context.Context, ex *Exchange) error {
	if s.Fn == nil {
		return nil
	}
	return s.Fn(ctx, ex)
}

// Chain compõe VÁRIOS estágios num ÚNICO slot da pipeline (AOS-280). A pipeline
// tem uma cadeia FIXA de cinco papéis ([Stages]) e um só ponto de extensão por
// papel; quando um papel é servido por mais do que um controlo — o caso do
// ROTEAMENTO, onde a guarda de soberania/failover (AOS-058) impõe a fronteira e o
// router cost/load-aware (AOS-059) refina DENTRO dela — é aqui que se encadeiam,
// sem quebrar a ordem nem o contrato de [Stage].
//
// # Semântica (a mesma da pipeline, um nível abaixo)
//
//   - corre os estágios por ORDEM de composição sobre o MESMO [Exchange] (o que um
//     resolve é a entrada do seguinte — ver a regra «resolvida-primeiro» do estágio
//     de roteamento);
//   - PARA no primeiro que recuse e propaga o erro TAL QUAL (sem o envolver): a
//     pipeline já o etiqueta com o nome do slot em [StageError], e envolvê-lo aqui
//     partiria o errors.Is dos sentinelas de cada estágio (ex.: o cross-border
//     bloqueado do failover);
//   - estágios nil são saltados (nunca um panic por um seam não composto).
//
// O NOME é o do SLOT (ex.: "roteamento"), não a concatenação dos nomes: o rasto de
// decisões e o [StageError] continuam a indexar o papel, e cada estágio encadeado
// regista a SUA decisão no Exchange com o nome que já usava.
type Chain struct {
	// StageName é o nome canónico do slot que esta cadeia serve.
	StageName string
	// Stages são os estágios encadeados, por ordem de execução.
	Stages []Stage
}

// Name implementa [Stage] com o nome do slot.
func (c Chain) Name() string {
	if c.StageName == "" {
		return "chain"
	}
	return c.StageName
}

// Process implementa [Stage]: corre a cadeia por ordem e PARA fail-closed no
// primeiro estágio que recuse (os seguintes não correm).
func (c Chain) Process(ctx context.Context, ex *Exchange) error {
	for _, st := range c.Stages {
		if st == nil {
			continue
		}
		if err := st.Process(ctx, ex); err != nil {
			return err
		}
	}
	return nil
}

// DenyStage é um estágio que recusa SEMPRE com a razão dada — a mecânica
// fail-closed que os estágios reais (ex.: allowlist de AOS-058) usam. Demonstra
// que uma recusa aborta a chamada antes de o provider ser invocado.
type DenyStage struct {
	StageName string
	Err       error
}

// Name implementa [Stage].
func (d DenyStage) Name() string {
	if d.StageName == "" {
		return "deny"
	}
	return d.StageName
}

// Process implementa [Stage] recusando fail-closed.
func (d DenyStage) Process(_ context.Context, ex *Exchange) error {
	ex.record(d.Name(), "deny", "recusa fail-closed")
	if d.Err != nil {
		return d.Err
	}
	return ErrDenied
}
