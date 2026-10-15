MODEL = "claude-haiku-5-5"
TIER_LOW = "<=100K"
TIER_HIGH = ">100K"
TIER_THRESHOLD = 100_000
SEARCH_USD = 0.01
MILLION = 1_000_000
RATES_PER_MTOK = {
    TIER_LOW: {"input": 0.10, "output": 0.50, "cache_write_5m": 0.125, "cache_write_1h": 0.20, "cache_read": 0.01},
    TIER_HIGH: {"input": 0.50, "output": 2.50, "cache_write_5m": 0.625, "cache_write_1h": 1.00, "cache_read": 0.05},
}


def prompt_tokens(tokens):
    return tokens["input"] + tokens["cache_write_5m"] + tokens["cache_write_1h"] + tokens["cache_read"]


def tier_of(tokens):
    return TIER_HIGH if prompt_tokens(tokens) > TIER_THRESHOLD else TIER_LOW


def cost_usd(tokens, searches=0):
    rates = RATES_PER_MTOK[tier_of(tokens)]
    return sum(tokens[k] * rates[k] for k in rates) / MILLION + searches * SEARCH_USD
