package taint

// Join é o least-upper-bound (⊔) do reticulado de confiança {Trusted ⊑ Untrusted}:
//
//	trusted  ⊔ trusted  = trusted
//	trusted  ⊔ untrusted = untrusted
//	untrusted ⊔ trusted  = untrusted
//	untrusted ⊔ untrusted = untrusted
//
// É a regra estrutural da propagação: a combinação de dois dados assume o rótulo
// MAIS RESTRITIVO. Não há entrada que produza trusted a partir de um operando
// untrusted — não existe caminho que "lave" o untrusted. Determinista e comutativa.
func Join(a, b Label) Label {
	if a.IsTrusted() && b.IsTrusted() {
		return Trusted
	}
	return Untrusted
}

// JoinAll aplica [Join] a uma sequência de rótulos. O elemento identidade é
// [Trusted] (o mínimo do reticulado): sem rótulos devolve [Trusted]. O resultado
// é [Untrusted] assim que UM único rótulo seja untrusted. Para derivar o rótulo de
// um dado a partir dos seus pais prefira [Derive], que é fail-closed sem pais.
func JoinAll(labels ...Label) Label {
	r := Trusted
	for _, l := range labels {
		r = Join(r, l)
	}
	return r
}

// Value é um dado com taint: o payload opaco, o seu [Label] e a PROVENIÊNCIA — as
// origens que contribuíram para ele. É imutável de fora (campos não-exportados);
// só [FromOrigin] e [Derive] o constroem, garantindo que o rótulo é sempre
// coerente com a proveniência e nunca pode ser elevado a trusted por atribuição.
type Value struct {
	label   Label
	origins []Origin
	payload []byte
}

// FromOrigin marca um dado na ORIGEM (o ponto de entrada no sistema). O rótulo é
// [LabelFor](o) e a proveniência é exactamente {o}. O payload é COPIADO (o Value
// não partilha o array do chamador). É a única forma de um Value NASCER trusted —
// e só se a origem for trusted.
func FromOrigin(o Origin, payload []byte) Value {
	return Value{
		label:   LabelFor(o),
		origins: []Origin{o},
		payload: cloneBytes(payload),
	}
}

// Derive produz um NOVO Value derivado de um ou mais pais. O rótulo é o [Join] de
// todos os rótulos dos pais e a proveniência é a UNIÃO (deduplicada, ordem estável
// de primeira aparição) das proveniências dos pais — o forense sobrevive à
// derivação (ASI06). Um Value derivado de (pelo menos um) pai untrusted é
// untrusted; NÃO há caminho que o torne trusted.
//
// FAIL-CLOSED: derivar SEM pais devolve um Value [Untrusted] com proveniência
// vazia — um dado sem qualquer origem confiável que o avalize não é confiável.
func Derive(payload []byte, parents ...Value) Value {
	if len(parents) == 0 {
		return Value{label: Untrusted, origins: nil, payload: cloneBytes(payload)}
	}
	label := parents[0].label
	origins := make([]Origin, 0, len(parents))
	seen := make(map[Origin]struct{})
	appendOrigins := func(os []Origin) {
		for _, o := range os {
			if _, dup := seen[o]; dup {
				continue
			}
			seen[o] = struct{}{}
			origins = append(origins, o)
		}
	}
	appendOrigins(parents[0].origins)
	for _, p := range parents[1:] {
		label = Join(label, p.label)
		appendOrigins(p.origins)
	}
	return Value{label: label, origins: origins, payload: cloneBytes(payload)}
}

// Label devolve o rótulo de confiança do valor.
func (v Value) Label() Label { return v.label }

// IsUntrusted é um atalho para v.Label().IsUntrusted().
func (v Value) IsUntrusted() bool { return v.label.IsUntrusted() }

// IsTrusted é um atalho para v.Label().IsTrusted().
func (v Value) IsTrusted() bool { return v.label.IsTrusted() }

// Origins devolve uma CÓPIA da proveniência (as origens que contribuíram para o
// valor). Cópia defensiva: o chamador não pode mutar o estado interno.
func (v Value) Origins() []Origin {
	if len(v.origins) == 0 {
		return nil
	}
	out := make([]Origin, len(v.origins))
	copy(out, v.origins)
	return out
}

// Payload devolve uma CÓPIA do conteúdo opaco.
func (v Value) Payload() []byte { return cloneBytes(v.payload) }

// cloneBytes devolve uma cópia (nil→nil) para não partilhar arrays com o chamador.
func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
