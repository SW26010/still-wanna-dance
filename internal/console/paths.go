package console

import "still-wanna-dance/internal/apppaths"

func (c *Console) files() apppaths.Paths { return apppaths.ForConfig(c.configPath) }
