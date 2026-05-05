"""Additional branch tests for services/pdf_extraction_service.py."""

from __future__ import annotations

import builtins
import importlib
import types
from unittest.mock import patch

import pytest

from services.pdf_extraction_service import (
    _extract_pdf_text,
    _fast_regex_extract,
    _llm_fallback_extract,
    _docling_extract,
    _normalize_title,
    extract_pdf_content,
)


def _build_fake_fitz_module():
    class FakePage:
        def __init__(self, text: str, blocks: list[dict]):
            self._text = text
            self._blocks = blocks

        def get_text(self, mode=None):
            if mode == "dict":
                return {"blocks": self._blocks}
            return self._text

    class FakeDoc:
        def __init__(self, pages):
            self._pages = pages

        def __len__(self):
            return len(self._pages)

        def __iter__(self):
            return iter(self._pages)

    class FakeFitz:
        def open(self, stream=None, filetype=None):
            return FakeDoc(
                [
                    FakePage(
                        "Intro page title",
                        [
                            {
                                "type": 0,
                                "bbox": [0, 0, 10, 10],
                                "lines": [{"spans": [{"text": "Intro"}, {"text": "text"}]}],
                            }
                        ],
                    ),
                    FakePage("Second page", []),
                ]
            )

    fake = types.ModuleType("fitz")
    fake.open = FakeFitz().open
    return fake


def _build_fake_pypdf_reader(text: str):
    class FakePage:
        def extract_text(self):
            return text

    class FakeReader:
        def __init__(self, *_args, **_kwargs):
            self.pages = [FakePage()]

    fake = types.ModuleType("pypdf")
    setattr(fake, "PdfReader", FakeReader)
    return fake


def test_normalize_title_collapses_separators():
    assert _normalize_title("Deep_Learning___Paper  .pdf") == "Deep Learning Paper"


def test_fast_regex_extract_falls_back_to_filename_title():
    result = _fast_regex_extract("no meta", "my_paper_name.PDF")
    assert result["title"] == "my paper name"
    assert result["doi"] is None
    assert result["year"] is None


def test_extract_pdf_text_uses_pymupdf_path():
    fake = _build_fake_fitz_module()
    with patch.dict("sys.modules", {"fitz": fake}):
        text, first_page, pages, blocks, used = _extract_pdf_text(b"dummy")

    assert used is True
    assert pages == 2
    assert "Intro text" in text
    assert first_page == "Intro text"
    assert blocks and blocks[0]["text"].startswith("Intro")


def test_extract_pdf_text_falls_back_to_pypdf_on_fitz_import_error():
    fake_reader = _build_fake_pypdf_reader("Fallback text")

    original_import = builtins.__import__

    def _importer(name, globals=None, locals=None, fromlist=(), level=0):
        if name == "fitz":
            raise ImportError("no fitz")
        if name == "pypdf":
            return fake_reader
        return original_import(name, globals, locals, fromlist, level)

    with patch("builtins.__import__", side_effect=_importer):
        text, first_page, pages, blocks, used = _extract_pdf_text(b"dummy")

    assert used is False
    assert pages == 1
    assert first_page == "Fallback text"
    assert text == "Fallback text"


def test_llm_fallback_extract_returns_empty_when_langextract_missing():
    with patch("services.pdf_extraction_service.importlib.import_module", side_effect=ImportError):
        result = _llm_fallback_extract("text")
    assert result == {}


def test_llm_fallback_extract_returns_empty_for_blank_safe_text():
    assert _llm_fallback_extract("   ") == {}


def test_llm_fallback_extract_reads_extractions_from_langextract():
    class _Extraction:
        def __init__(
            self,
            extraction_class: str,
            extraction_text: str,
            attributes=None,
            **_kwargs,
        ):
            self.extraction_class = extraction_class
            self.extraction_text = extraction_text
            self.attributes: dict[str, object] = {}

    class _Data:
        class Extraction(_Extraction):
            pass

        class ExampleData:
            def __init__(self, *, text, extractions):
                self.text = text
                self.extractions = extractions

    module = types.ModuleType("langextract")
    module.extract = lambda text_or_documents, prompt_description, examples, model_id: types.SimpleNamespace(  # type: ignore[call-arg]
        extractions=[
            _Data.Extraction("title", "Fallback title"),
            _Data.Extraction("author", "Jane"),
            _Data.Extraction("author", "John"),
            _Data.Extraction("abstract", "Some abstract"),
            _Data.Extraction("year", "2021"),
            _Data.Extraction("doi", "10.5555/test"),
        ]
    )
    module.data = _Data

    with patch.dict("sys.modules", {"langextract": module}):
        result = _llm_fallback_extract("first page")

    assert result["title"] == "Fallback title"
    assert result["authors"] == ["Jane", "John"]
    assert result["year"] == 2021
    assert result["doi"] == "10.5555/test"


def test_llm_fallback_extract_uses_year_fallback_when_year_not_integer():
    class _Extraction:
        def __init__(self, extraction_class: str, extraction_text: str, **_kwargs):
            self.extraction_class = extraction_class
            self.extraction_text = extraction_text
            self.attributes: dict[str, object] = {}

    class _Data:
        class Extraction(_Extraction):
            pass

        class ExampleData:
            def __init__(self, *, text, extractions):
                self.text = text
                self.extractions = extractions

    module = types.ModuleType("langextract")
    module.extract = lambda text_or_documents, prompt_description, examples, model_id: types.SimpleNamespace(  # type: ignore[call-arg]
        extractions=[_Data.Extraction("year", "Published in 2021")]
    )
    module.data = _Data

    with patch.dict("sys.modules", {"langextract": module}):
        result = _llm_fallback_extract("first page")

    assert result["year"] == 2021


def test_extract_pdf_text_handles_string_page_dict_and_non_text_blocks():
    class FakePage:
        def __init__(self, page_dict, fallback_text=""):
            self._page_dict = page_dict
            self._fallback_text = fallback_text

        def get_text(self, mode=None):
            if mode == "dict":
                return self._page_dict
            return self._fallback_text

    class FakeDoc:
        def __len__(self):
            return 2

        def __iter__(self):
            return iter(
                [
                    FakePage("String page dict"),
                    FakePage(
                        {
                            "blocks": [
                                {"type": 1, "bbox": [0, 0, 1, 1], "lines": []},
                                {
                                    "type": 0,
                                    "bbox": [0, 0, 10, 10],
                                    "lines": [{"spans": [{"text": "Results"}]}],
                                },
                            ]
                        }
                    ),
                ]
            )

    fake = types.ModuleType("fitz")
    fake.open = lambda stream=None, filetype=None: FakeDoc()

    with patch.dict("sys.modules", {"fitz": fake}):
        text, first_page, pages, blocks, used = _extract_pdf_text(b"dummy")

    assert used is True
    assert pages == 2
    assert first_page == "String page dict"
    assert blocks == [{"page": 1, "text": "Results", "bbox": [0, 0, 10, 10]}]


def test_extract_pdf_content_without_docling_and_with_llm_fallback(monkeypatch):
    with patch("services.pdf_extraction_service._docling_extract", return_value=None):
        with patch(
            "services.pdf_extraction_service._extract_pdf_text",
            return_value=("meta", "Abstract page text", 2, [], False),
        ):
            with patch.object(
                importlib,
                "import_module",
                side_effect=ImportError("no langextract"),
            ):
                paper = extract_pdf_content(b"bytes", "tiny.pdf")

    assert paper["paper"]["title"] == "tiny"
    assert paper["extractionInfo"]["usedDocling"] is False


def test_docling_extract_returns_structured_payload():
    class FakePdfFormatOption:
        def __init__(self, pipeline_options=None):
            self.pipeline_options = pipeline_options

    class FakeInputFormat:
        PDF = "pdf"

    class FakePdfPipelineOptions:
        def __init__(self):
            self.do_ocr = False
            self.do_table_structure = False

    class FakePipeline:
        def __init__(self):
            self.pipeline_options = None

    class FakeDoc:
        def __init__(self):
            self._labels = [("title", "Introduction"), ("heading", "Methods")]

        def export_to_markdown(self):
            return "# Intro"

        def iterate_items(self):
            for label, text in self._labels:
                yield type("Node", (), {"label": label, "text": text}), 0

    class FakeResult:
        def __init__(self):
            self.document = FakeDoc()

    class FakeConverter:
        def __init__(self, format_options=None):
            self.format_options = format_options

        def convert(self, source):
            return FakeResult()

    fake_docling_base = types.ModuleType("docling")
    fake_models = types.ModuleType("docling.datamodel")
    fake_base_models = types.ModuleType("docling.datamodel.base_models")
    fake_pipeline = types.ModuleType("docling.datamodel.pipeline_options")
    fake_converter = types.ModuleType("docling.document_converter")
    fake_document = types.ModuleType("docling.datamodel.document")

    fake_base_models.InputFormat = FakeInputFormat
    fake_pipeline.PdfPipelineOptions = FakePdfPipelineOptions
    fake_converter.DocumentConverter = FakeConverter
    fake_converter.PdfFormatOption = FakePdfFormatOption
    fake_document.DocumentStream = type(
        "DocumentStream",
        (),
        {"__init__": lambda self, name, stream: None},
    )
    fake_docling_base.datamodel = fake_models
    fake_models.base_models = fake_base_models
    fake_models.pipeline_options = fake_pipeline
    fake_docling_base.datamodel.document = fake_document

    fake_docling_base.datamodel.base_models = fake_base_models
    fake_docling_base.datamodel.pipeline_options = fake_pipeline
    fake_docling_base.document_converter = fake_converter

    with patch.dict(
        "sys.modules",
        {
            "docling": fake_docling_base,
            "docling.datamodel": fake_models,
            "docling.datamodel.base_models": fake_base_models,
            "docling.datamodel.pipeline_options": fake_pipeline,
            "docling.document_converter": fake_converter,
            "docling.datamodel.document": fake_document,
        },
        clear=False,
    ):
        result = _docling_extract(b"pdf-bytes", "paper.pdf")

    assert result is not None
    assert result["full_text"] == "# Intro"
    assert result["docling_meta"]["version"] == "2.3.0+"
    assert len(result["structure_map"]) == 2
    assert result["structure_map"][0]["label"] == "Introduction"


def test_docling_extract_returns_none_when_dependencies_missing():
    def _importer(name, globals=None, locals=None, fromlist=(), level=0):
        raise ImportError(f"{name} unavailable")

    with patch("builtins.__import__", side_effect=_importer):
        result = _docling_extract(b"pdf-bytes", "paper.pdf")

    assert result is None


def test_extract_pdf_content_prefers_docling_payload():
    with patch(
        "services.pdf_extraction_service._docling_extract",
        return_value={
            "full_text": "Docling output 10.1234/sample 2021",
            "structure_map": [{"label": "intro", "page": 0, "bbox": None}],
            "docling_meta": {"version": "2.3.0+"},
        },
    ):
        with patch(
            "services.pdf_extraction_service._extract_pdf_text",
            return_value=("fallback", "First page text", 3, [], False),
        ):
            paper = extract_pdf_content(b"bytes", "my_long_paper_title_2021.pdf")

    assert paper["extractionInfo"]["usedDocling"] is True
    assert paper["fullText"] == "Docling output 10.1234/sample 2021"
    assert paper["structureMap"] == [{"label": "intro", "page": 0, "bbox": None}]
    assert paper["pageCount"] == 3


def test_extract_pdf_content_builds_legacy_structure_map_from_blocks():
    with patch("services.pdf_extraction_service._docling_extract", return_value=None):
        with patch(
            "services.pdf_extraction_service._extract_pdf_text",
            return_value=(
                "Results\nUseful text",
                "Results",
                1,
                [{"page": 0, "text": "Results", "bbox": [0, 0, 10, 10]}],
                True,
            ),
        ):
            paper = extract_pdf_content(b"bytes", "results_paper.pdf")

    assert paper["extractionInfo"]["usedDocling"] is False
    assert paper["structureMap"] == [{"label": "results", "page": 0, "bbox": [0, 0, 10, 10]}]


def test_extract_pdf_text_returns_empty_tuple_on_total_failure():
    original_import = builtins.__import__

    def _importer(name, globals=None, locals=None, fromlist=(), level=0):
        if name in {"fitz", "pypdf"}:
            raise ImportError(f"{name} unavailable")
        return original_import(name, globals, locals, fromlist, level)

    with patch("builtins.__import__", side_effect=_importer):
        text, first_page, pages, blocks, used = _extract_pdf_text(b"not a pdf")

    assert text == ""
    assert first_page == ""
    assert pages == 0
    assert blocks == []
    assert used is False
