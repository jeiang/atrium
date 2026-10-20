import pytest
from agent_tool_helpers import make_ctx
from helpers import make_config, memory_db

from atrium.agent.tools import contacts
from atrium.agent.tools.base import NotConfigured

VCF = """BEGIN:VCARD
VERSION:3.0
FN:Ada Lovelace
EMAIL:ada@example.test
TEL:+15550001
ORG:Analytical Engines
END:VCARD
BEGIN:VCARD
VERSION:3.0
FN:Grace Hopper
EMAIL:grace@navy.test
END:VCARD
"""


def test_lookup_matches_people_only(tmp_path):
    (tmp_path / "book.vcf").write_text(VCF)
    ctx = make_ctx(memory_db(), make_config(tmp_path, contacts_dir=tmp_path))
    out = contacts.contacts_lookup(ctx, {"query": "ada"})
    assert out["results"] == [
        {"name": "Ada Lovelace", "emails": ["ada@example.test"], "phones": ["+15550001"], "org": "Analytical Engines"}
    ]
    assert contacts.contacts_lookup(ctx, {"query": "navy.test"})["results"][0]["name"] == "Grace Hopper"
    assert len(contacts.contacts_lookup(ctx, {"query": "e", "limit": 1})["results"]) == 1


def test_lookup_requires_directory(tmp_path):
    ctx = make_ctx(memory_db(), make_config(tmp_path))
    with pytest.raises(NotConfigured):
        contacts.contacts_lookup(ctx, {"query": "ada"})
