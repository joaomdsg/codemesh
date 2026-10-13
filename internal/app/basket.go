package app

import "github.com/joaomdsg/codemesh/internal/handoff"

// Basket is what the user collected to hand to a coding agent.
func (a *App) Basket() handoff.Basket { return a.basket.Basket() }

// BasketAdd appends an item and returns it with its ID.
func (a *App) BasketAdd(it handoff.Item) (handoff.Item, error) {
	it, err := a.basket.Add(it)
	a.publish("")
	return it, err
}

// BasketRemove drops the item with the given ID.
func (a *App) BasketRemove(id string) error {
	err := a.basket.Remove(id)
	a.publish("")
	return err
}

// BasketMove puts the item at position to.
func (a *App) BasketMove(id string, to int) error {
	err := a.basket.Move(id, to)
	a.publish("")
	return err
}

// BasketIntro sets the line that states the prompt's goal.
func (a *App) BasketIntro(intro string) error {
	err := a.basket.SetIntro(intro)
	a.publish("")
	return err
}

// BasketText is the basket as one prompt, naming each repo.
func (a *App) BasketText() string { return handoff.Render(a.Basket(), a.basketContext()) }

// BasketGroups is the basket in the order the prompt lists it.
func (a *App) BasketGroups() []handoff.Group { return handoff.Groups(a.Basket(), a.basketContext()) }

func (a *App) basketContext() handoff.Context {
	names := map[string]string{}
	repos, _ := a.ws.Repos()
	for _, r := range repos {
		names[r.ID] = r.Name
	}
	return handoff.Context{Repos: names}
}
