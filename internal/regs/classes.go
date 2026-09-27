package regs

// Prototype controlled vocabularies of class/type (standards of identity)
// designations, normalized (lowercase, single spaces). These are a
// representative subset of 27 CFR Part 5 subpart I (spirits), Part 4 subpart C
// (wine), and Part 7 subpart I (malt); the full vocabulary is large and flagged
// as a stretch goal in research.txt 2.4.2. Matching is word/phrase-boundary
// aware so short terms ("gin", "rum", "ale", "ipa") do not match inside longer
// words ("engine", "drum", "whale").

// DistilledSpiritsClasses lists common distilled-spirits class/type
// designations (27 CFR Part 5 subpart I). Longest-first so a specific
// designation ("straight bourbon whiskey") is preferred over a generic one.
var DistilledSpiritsClasses = []string{
	"straight rye malt whiskey", "straight rye malt whisky",
	"straight bourbon whiskey", "straight bourbon whisky",
	"straight rye whiskey", "straight rye whisky",
	"straight wheat whiskey", "straight wheat whisky",
	"straight malt whiskey", "straight malt whisky",
	"straight corn whiskey", "straight corn whisky",
	"straight whiskey", "straight whisky",
	"blend of straight whiskey", "blend of straight whisky",
	"blended whiskey", "blended whisky",
	"bourbon whiskey", "bourbon whisky",
	"rye whiskey", "rye whisky",
	"wheat whiskey", "wheat whisky",
	"malt whiskey", "malt whisky",
	"rye malt whiskey", "rye malt whisky",
	"corn whiskey", "corn whisky",
	"whiskey", "whisky",
	"distilled gin", "gin",
	"vodka",
	"rum",
	"fruit brandy", "neutral brandy", "brandy",
	"tequila",
	"mezcal",
	"liqueur", "cordial",
	"aquavit", "akvavit",
	"schnapps",
	"specialty spirit",
}

// WineClasses lists common wine class/type designations (27 CFR Part 4 subpart
// C) plus common varietal/type terms.
var WineClasses = []string{
	"sparkling wine", "table wine", "dessert wine",
	"red wine", "white wine", "rose wine",
	"cabernet sauvignon", "sauvignon blanc", "pinot noir",
	"chardonnay", "merlot", "zinfandel", "riesling",
}

// MaltBeverageClasses lists common malt-beverage class/type designations
// (27 CFR Part 7 subpart I).
var MaltBeverageClasses = []string{
	"malt beverage", "india pale ale", "ipa",
	"pilsner", "porter", "stout", "bock", "weissbier", "hefeweizen",
	"ale", "lager", "beer",
}
