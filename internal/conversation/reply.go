package conversation

// Reply is a turn's answer plus its optional stats footer, kept apart so a backend can style the footer.
type Reply struct {
	Text   string
	Footer string
}

// Plain joins the footer below the text for backends without rich formatting.
func (r Reply) Plain() string {
	if r.Footer == "" {
		return r.Text
	}
	return r.Text + "\n\n" + r.Footer
}
