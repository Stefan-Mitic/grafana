// import React from 'react';

import { CalculateFieldHelper } from './CalculateFieldHelper';
import { ConcatenateHelper } from './ConcatenateHelper';
import { ConfigFromQueryHelper } from './ConfigFromQueryHelper';
import { ConvertFieldTypeHelper } from './ConvertFieldTypeHelper';
import { CreateHeatmapHelp } from './CreateHeatmapHelp';
import { ExtractFieldsHelper } from './ExtractFieldsHelper';
import { FieldLookupHelper } from "./FieldLookupHelper";
import { FilterFieldsByNameHelper } from "./FilterFieldsByNameHelper";

interface Helper {
  [key: string]: JSX.Element | string;
}

// JEV: what about "field lookup" transform?
// JEV: add link to docs at the end of each helper?
const helperContent: Helper = {
  calculateField: CalculateFieldHelper(),
  concatenate: ConcatenateHelper(),
  // There is a discrepancy between the name of the id and the name of the transformer
  configFromData: ConfigFromQueryHelper(),
  convertFieldType: ConvertFieldTypeHelper(),
  heatmap: CreateHeatmapHelp(),
  extractFields: ExtractFieldsHelper(),
  fieldLookup: FieldLookupHelper(),
  filterFieldsByName: FilterFieldsByNameHelper(),
};

// JEV: add logic for no helper/string content
export function getHelperContent(id: string): JSX.Element | string {
  const defaultMessage = 'u broke it, u buy it';

  if (id in helperContent) {
    return helperContent[id];
  }

  return defaultMessage;
}
